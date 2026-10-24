// Package kube discovers Bee nodes in a local k3d cluster created by
// beekeeper and gives read access to their API (through the API-server
// service proxy) and to their container logs.
//
// Safety: bee-scope refuses to talk to any cluster that is not a local k3d
// cluster (context name "k3d-*" and a loopback API server), because the
// default kube context on developer machines can point at a shared cluster.
package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"beescope/pkg/model"
)

// ErrNotLocal is returned when the selected context is not a local cluster.
var ErrNotLocal = errors.New("kube: refusing non-local cluster")

// Options configure the client.
type Options struct {
	Kubeconfig  string // empty = default loading rules
	Context     string // empty = current context
	Namespace   string
	AllowRemote bool // skip the local-cluster guard
	Selector    string
}

// Client is a guarded kube client. It reconnects by itself: when the
// cluster does not exist yet, is stopped, or is recreated (new port and
// certificates), calls fail and the next call reloads the kubeconfig.
type Client struct {
	opts    Options
	Context string
	Server  string

	mu       sync.Mutex
	cur      *conn
	lastLoad time.Time
	loadErr  error
	http     *http.Client
}

type conn struct {
	cfg *rest.Config
	cs  *kubernetes.Clientset
	rt  http.RoundTripper
}

// reloadEvery rate-limits kubeconfig reloads after failures.
const reloadEvery = 3 * time.Second

// New loads the kubeconfig, applies the guard and builds a client. A missing
// or unreachable k3d cluster is not an error: the client keeps retrying, so
// bee-scope can start before the cluster exists. A context that fails the
// local-cluster guard is.
func New(opts Options) (*Client, error) {
	if opts.Selector == "" {
		opts.Selector = "app.kubernetes.io/name=bee"
	}
	if opts.Context != "" && !opts.AllowRemote && !strings.HasPrefix(opts.Context, "k3d-") {
		return nil, fmt.Errorf("%w: context %q is not a k3d context (use --allow-remote to override)", ErrNotLocal, opts.Context)
	}
	c := &Client{opts: opts, Context: opts.Context}
	c.http = &http.Client{Transport: roundTripper{c}}
	cn, ctxName, err := c.load()
	if errors.Is(err, ErrNotLocal) {
		return nil, err
	}
	if ctxName != "" {
		c.Context = ctxName
	}
	if err != nil {
		if opts.Context == "" {
			return nil, err
		}
		c.loadErr = err
		c.lastLoad = time.Now()
		return c, nil
	}
	c.cur, c.lastLoad, c.Server = cn, time.Now(), cn.cfg.Host
	return c, nil
}

// Err returns the last connection error, or nil when connected.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil {
		return nil
	}
	return c.loadErr
}

// load builds a connection for the current options. Callers hold c.mu,
// except New, which runs before the client is shared.
func (c *Client) load() (*conn, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.opts.Kubeconfig != "" {
		rules.ExplicitPath = c.opts.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: c.opts.Context}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	ctxName := c.opts.Context
	if ctxName == "" {
		ctxName = raw.CurrentContext
	}
	if _, ok := raw.Contexts[ctxName]; !ok {
		return nil, ctxName, fmt.Errorf("kube context %q not found (the cluster does not exist yet?)", ctxName)
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, ctxName, fmt.Errorf("kube client config: %w", err)
	}
	if !c.opts.AllowRemote {
		if err := guard(ctxName, cfg.Host); err != nil {
			return nil, ctxName, err
		}
	}
	cfg.QPS = 50
	cfg.Burst = 100
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, ctxName, fmt.Errorf("kube clientset: %w", err)
	}
	rt, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, ctxName, fmt.Errorf("kube transport: %w", err)
	}
	return &conn{cfg: cfg, cs: cs, rt: rt}, ctxName, nil
}

// get returns the current connection, reloading the kubeconfig when there
// is none (rate-limited).
func (c *Client) get() (*conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil {
		return c.cur, nil
	}
	if time.Since(c.lastLoad) < reloadEvery && c.loadErr != nil {
		return nil, c.loadErr
	}
	cn, _, err := c.load()
	c.lastLoad = time.Now()
	if err != nil {
		c.loadErr = err
		return nil, err
	}
	c.cur, c.loadErr, c.Server = cn, nil, cn.cfg.Host
	return cn, nil
}

// Reset drops the connection so the next call reloads the kubeconfig. Call
// it after a call failed or after the cluster was recreated.
func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = nil
	c.lastLoad = time.Time{}
}

// Clientset returns the current clientset.
func (c *Client) Clientset() (*kubernetes.Clientset, error) {
	cn, err := c.get()
	if err != nil {
		return nil, err
	}
	return cn.cs, nil
}

type roundTripper struct{ c *Client }

func (r roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cn, err := r.c.get()
	if err != nil {
		return nil, err
	}
	resp, err := cn.rt.RoundTrip(req)
	if err != nil && isConnErr(err) {
		r.c.Reset()
	}
	return resp, err
}

// isConnErr reports errors that mean the API server moved or went away.
func isConnErr(err error) bool {
	var ne net.Error
	var oe *net.OpError
	return errors.As(err, &oe) || (errors.As(err, &ne) && !ne.Timeout()) || strings.Contains(err.Error(), "x509") || strings.Contains(err.Error(), "certificate")
}

func guard(ctxName, host string) error {
	if !strings.HasPrefix(ctxName, "k3d-") {
		return fmt.Errorf("%w: context %q is not a k3d context (use --allow-remote to override)", ErrNotLocal, ctxName)
	}
	u, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("%w: cannot parse API server %q", ErrNotLocal, host)
	}
	h := u.Hostname()
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return nil
	}
	// k3d sometimes writes 0.0.0.0, which also only ever dials this machine.
	if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return nil
	}
	return fmt.Errorf("%w: API server %q is not loopback (use --allow-remote to override)", ErrNotLocal, host)
}

// HTTPClient returns the authenticated client used for service-proxy calls.
// It always uses the current connection.
func (c *Client) HTTPClient() *http.Client { return c.http }

// Namespace returns the watched namespace.
func (c *Client) Namespace() string {
	_, ns := c.Target()
	return ns
}

// Discover lists Bee API services and their pods.
func (c *Client) Discover(ctx context.Context) ([]model.NodeInfo, error) {
	cn, err := c.get()
	if err != nil {
		return nil, err
	}
	ns := c.Namespace()
	svcs, err := cn.cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: c.opts.Selector})
	if err != nil {
		if ctx.Err() == nil {
			c.Reset()
		}
		return nil, fmt.Errorf("list services: %w", err)
	}
	pods, err := cn.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: c.opts.Selector})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	podFor := map[string]string{}
	for _, p := range pods.Items {
		if inst := p.Labels["app.kubernetes.io/instance"]; inst != "" && podRunning(&p) {
			podFor[inst] = p.Name
		}
	}
	var out []model.NodeInfo
	for _, s := range svcs.Items {
		if strings.HasSuffix(s.Name, "-headless") || !hasPort(&s, "api") {
			continue
		}
		base := strings.TrimRight(cn.cfg.Host, "/") + fmt.Sprintf("/api/v1/namespaces/%s/services/%s:api/proxy", ns, s.Name)
		out = append(out, model.NodeInfo{Name: s.Name, Pod: podFor[s.Name], Group: groupOf(s.Name), BaseURL: base})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func podRunning(p *corev1.Pod) bool {
	return p.DeletionTimestamp == nil && (p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending)
}

func hasPort(s *corev1.Service, name string) bool {
	for _, p := range s.Spec.Ports {
		if p.Name == name {
			return true
		}
	}
	return false
}

// groupOf derives the beekeeper node group from a service name ("bee-3" ->
// "bee", "bootnode-0" -> "bootnode").
func groupOf(name string) string {
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		return name[:i]
	}
	return name
}

// FollowLogs streams a pod's container log with kube timestamps.
func (c *Client) FollowLogs(ctx context.Context, pod string, since time.Duration) (io.ReadCloser, error) {
	secs := int64(since.Seconds())
	opts := &corev1.PodLogOptions{Follow: true, Timestamps: true}
	if secs > 0 {
		opts.SinceSeconds = &secs
	}
	cn, err := c.get()
	if err != nil {
		return nil, err
	}
	rc, err := cn.cs.CoreV1().Pods(c.Namespace()).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("follow logs %s: %w", pod, err)
	}
	return rc, nil
}

// ContextInfo describes a kubeconfig context.
type ContextInfo struct {
	Name    string `json:"name"`
	Server  string `json:"server"`
	Local   bool   `json:"local"`   // passes the local-cluster guard
	Current bool   `json:"current"` // the kubeconfig's current-context
	Reason  string `json:"reason"`  // why it is not local
}

// Contexts lists the kubeconfig's contexts. It never returns credentials.
func (c *Client) Contexts() ([]ContextInfo, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.opts.Kubeconfig != "" {
		rules.ExplicitPath = c.opts.Kubeconfig
	}
	raw, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	var out []ContextInfo
	for name, kc := range raw.Contexts {
		ci := ContextInfo{Name: name, Current: name == raw.CurrentContext}
		if cl, ok := raw.Clusters[kc.Cluster]; ok {
			ci.Server = cl.Server
		}
		if err := guard(name, ci.Server); err != nil {
			ci.Reason = strings.TrimPrefix(err.Error(), ErrNotLocal.Error()+": ")
		} else {
			ci.Local = true
		}
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Target returns the context and namespace in use.
func (c *Client) Target() (kubeContext, namespace string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opts.Context, c.opts.Namespace
}

// AllowRemote reports whether the local-cluster guard is disabled.
func (c *Client) AllowRemote() bool { return c.opts.AllowRemote }

// SetTarget switches to another context and namespace. The context must
// exist and pass the local-cluster guard (unless AllowRemote).
func (c *Client) SetTarget(kubeContext, namespace string) error {
	if kubeContext == "" || namespace == "" {
		return errors.New("context and namespace are required")
	}
	if !c.opts.AllowRemote && !strings.HasPrefix(kubeContext, "k3d-") {
		return fmt.Errorf("%w: context %q is not a k3d context (start bee-scope with --allow-remote to allow it)", ErrNotLocal, kubeContext)
	}
	ctxs, err := c.Contexts()
	if err != nil {
		return err
	}
	found := false
	for _, ci := range ctxs {
		if ci.Name == kubeContext {
			found = true
			if !ci.Local && !c.opts.AllowRemote {
				return fmt.Errorf("%w: %s (start bee-scope with --allow-remote to allow it)", ErrNotLocal, ci.Reason)
			}
		}
	}
	if !found {
		return fmt.Errorf("kube context %q not found", kubeContext)
	}
	c.mu.Lock()
	c.opts.Context, c.opts.Namespace = kubeContext, namespace
	c.Context = kubeContext
	c.cur = nil
	c.lastLoad = time.Time{}
	c.mu.Unlock()
	return nil
}
