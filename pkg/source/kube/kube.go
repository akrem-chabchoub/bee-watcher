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

// Client is a guarded kube client.
type Client struct {
	opts    Options
	cfg     *rest.Config
	cs      *kubernetes.Clientset
	http    *http.Client
	Context string
	Server  string
}

// New loads the kubeconfig, applies the guard and builds a client.
func New(opts Options) (*Client, error) {
	if opts.Selector == "" {
		opts.Selector = "app.kubernetes.io/name=bee"
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		rules.ExplicitPath = opts.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: opts.Context}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	ctxName := opts.Context
	if ctxName == "" {
		ctxName = raw.CurrentContext
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube client config: %w", err)
	}
	if !opts.AllowRemote {
		if err := guard(ctxName, cfg.Host); err != nil {
			return nil, err
		}
	}
	cfg.QPS = 50
	cfg.Burst = 100
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube clientset: %w", err)
	}
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube http client: %w", err)
	}
	return &Client{opts: opts, cfg: cfg, cs: cs, http: hc, Context: ctxName, Server: cfg.Host}, nil
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
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: API server %q is not loopback (use --allow-remote to override)", ErrNotLocal, host)
}

// HTTPClient returns the authenticated client used for service-proxy calls.
func (c *Client) HTTPClient() *http.Client { return c.http }

// Namespace returns the watched namespace.
func (c *Client) Namespace() string { return c.opts.Namespace }

// Discover lists Bee API services and their pods.
func (c *Client) Discover(ctx context.Context) ([]model.NodeInfo, error) {
	svcs, err := c.cs.CoreV1().Services(c.opts.Namespace).List(ctx, metav1.ListOptions{LabelSelector: c.opts.Selector})
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	pods, err := c.cs.CoreV1().Pods(c.opts.Namespace).List(ctx, metav1.ListOptions{LabelSelector: c.opts.Selector})
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
		base := strings.TrimRight(c.cfg.Host, "/") + fmt.Sprintf("/api/v1/namespaces/%s/services/%s:api/proxy", c.opts.Namespace, s.Name)
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
	rc, err := c.cs.CoreV1().Pods(c.opts.Namespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("follow logs %s: %w", pod, err)
	}
	return rc, nil
}
