// Package control runs the local cluster for the Cluster tab: Docker, the
// k3d cluster (the same steps as beelocal's k8s-local), geth, Bee nodes
// through beekeeper, per-node restarts and beekeeper checks.
//
// Safety: every command targets the k3d context explicitly (kubectl
// --context, helm --kube-context, beekeeper --kubeconfig with a
// single-context file), so a different current context can never be
// touched. Jobs run one at a time and can be canceled.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"beescope/pkg/source/kube"
)

// ErrBusy is returned when a job is already running.
var ErrBusy = errors.New("another job is running")

// Config wires a Controller.
type Config struct {
	Kube        *kube.Client
	KubeContext string // k3d-<cluster>
	Namespace   string
	StateDir    string // settings and generated beekeeper config
	Log         *slog.Logger
	// OnJob is called when a job starts and ends (for the timeline).
	OnJob func(v JobView)
}

// Controller owns settings, jobs and status probes.
type Controller struct {
	cfg       Config
	cluster   string // k3d cluster name
	stateDir  string
	walletKey string

	mu         sync.Mutex
	settings   Settings
	jobs       []*Job // newest last
	current    *Job
	seq        int
	profiles   []Profile
	checks     []string
	profilesAt time.Time
	status     *Status
	statusAt   time.Time
}

// New returns a controller and fills in auto-detected settings.
func New(cfg Config) *Controller {
	augmentPath()
	c := &Controller{cfg: cfg, cluster: strings.TrimPrefix(cfg.KubeContext, "k3d-"), stateDir: cfg.StateDir}
	c.loadSettings()
	return c
}

// augmentPath adds the usual tool locations, since bee-scope may be started
// from Finder with a minimal PATH.
func augmentPath() {
	home, _ := os.UserHomeDir()
	extra := []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, "go", "bin"), "/Applications/Docker.app/Contents/Resources/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	if gp := os.Getenv("GOPATH"); gp != "" {
		extra = append([]string{filepath.Join(gp, "bin")}, extra...)
	}
	have := map[string]bool{}
	parts := filepath.SplitList(os.Getenv("PATH"))
	for _, p := range parts {
		have[p] = true
	}
	for _, p := range extra {
		if !have[p] {
			parts = append(parts, p)
		}
	}
	_ = os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
}

// Jobs returns the last jobs, newest first.
func (c *Controller) Jobs() []JobView {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]JobView, 0, len(c.jobs))
	for i := len(c.jobs) - 1; i >= 0; i-- {
		out = append(out, c.jobs[i].View())
	}
	return out
}

// Job finds a job by id.
func (c *Controller) Job(id string) (*Job, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, j := range c.jobs {
		if j.view.ID == id {
			return j, true
		}
	}
	return nil, false
}

// start registers and launches a job. fn runs the steps.
func (c *Controller) start(action, title string, steps []string, fn func(ctx context.Context, j *Job) error) (*Job, error) {
	c.mu.Lock()
	if c.current != nil {
		c.mu.Unlock()
		return nil, ErrBusy
	}
	c.seq++
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{cancel: cancel, done: make(chan struct{}), scrub: c.scrubber()}
	j.view = JobView{ID: "j" + strconv.Itoa(c.seq), Action: action, Title: title, State: JobRunning, StartedAt: time.Now()}
	for _, s := range steps {
		j.view.Steps = append(j.view.Steps, Step{Title: s, State: StepPending})
	}
	c.current = j
	c.jobs = append(c.jobs, j)
	if len(c.jobs) > 20 {
		c.jobs = c.jobs[len(c.jobs)-20:]
	}
	c.mu.Unlock()
	c.notify(j)
	go func() {
		defer close(j.done)
		err := fn(ctx, j)
		now := time.Now()
		j.mu.Lock()
		j.view.EndedAt = &now
		switch {
		case ctx.Err() != nil:
			j.view.State = JobCanceled
			j.view.Error = "canceled"
		case err != nil:
			j.view.State = JobFailed
			j.view.Error = j.scrub(err.Error())
		default:
			j.view.State = JobSucceeded
		}
		for i := range j.view.Steps {
			if j.view.Steps[i].State == StepRunning {
				j.view.Steps[i].State = StepFailed
			}
		}
		state, msg := j.view.State, j.view.Error
		j.mu.Unlock()
		if msg != "" {
			j.line("err", "✕ "+msg)
		} else {
			j.line("info", "✔ "+state)
		}
		cancel()
		c.mu.Lock()
		c.current = nil
		c.statusAt = time.Time{}
		c.mu.Unlock()
		c.notify(j)
	}()
	return j, nil
}

func (c *Controller) notify(j *Job) {
	if c.cfg.OnJob != nil {
		c.cfg.OnJob(j.View())
	}
}

var hex64 = regexp.MustCompile(`(?i)(0x)?[0-9a-f]{64}`)

// scrubber hides the beekeeper wallet key if a tool ever prints it.
func (c *Controller) scrubber() func(string) string {
	key := strings.ToLower(strings.TrimPrefix(c.walletKey, "0x"))
	return func(s string) string {
		if key == "" {
			return s
		}
		return hex64.ReplaceAllStringFunc(s, func(m string) string {
			if strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(m, "0x"), "0X")) == key {
				return "[wallet key redacted]"
			}
			return m
		})
	}
}

// Tool is a required command-line tool.
type Tool struct {
	Name string `json:"name"`
	Path string `json:"path"`
	OK   bool   `json:"ok"`
	Hint string `json:"hint"`
}

// NodeStatus is one Bee statefulset.
type NodeStatus struct {
	Name      string     `json:"name"`
	Group     string     `json:"group"`
	Replicas  int32      `json:"replicas"`
	Ready     int32      `json:"ready"`
	Image     string     `json:"image"`
	Pod       string     `json:"pod"`
	Phase     string     `json:"phase"`
	Restarts  int32      `json:"restarts"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	Reason    string     `json:"reason"`
}

// Status is GET /api/control/status.
type Status struct {
	Enabled        bool     `json:"enabled"`
	DisabledReason string   `json:"disabledReason"`
	Settings       Settings `json:"settings"`
	Tools          []Tool   `json:"tools"`
	Docker         struct {
		Running bool   `json:"running"`
		Error   string `json:"error"`
	} `json:"docker"`
	Cluster struct {
		Name            string `json:"name"`
		KubeContext     string `json:"kubeContext"`
		Namespace       string `json:"namespace"`
		Exists          bool   `json:"exists"`
		Running         bool   `json:"running"`
		RegistryRunning bool   `json:"registryRunning"`
		RegistryPort    string `json:"registryPort"`
		LocalImage      string `json:"localImage"` // image name for a locally built :latest
		Error           string `json:"error"`
	} `json:"cluster"`
	Geth struct {
		Installed bool `json:"installed"`
		Ready     bool `json:"ready"`
	} `json:"geth"`
	Nodes    []NodeStatus `json:"nodes"`
	Deployed struct {
		Groups map[string]int `json:"groups"`
		Image  string         `json:"image"`
	} `json:"deployed"`
	Profiles []Profile `json:"profiles"`
	Checks   []string  `json:"checks"`
	Hosts    []string  `json:"hostsMissing"`
	Job      *JobView  `json:"job"`
}

var toolHints = map[string]string{
	"docker":    "install Docker Desktop: https://www.docker.com/products/docker-desktop/",
	"k3d":       "brew install k3d",
	"kubectl":   "brew install kubectl",
	"helm":      "brew install helm",
	"beekeeper": "in the beekeeper repo: make binary && cp dist/beekeeper ~/go/bin/  (or set its path in Settings)",
	"jq":        "brew install jq",
	"make":      "xcode-select --install",
}

// Status probes Docker, k3d, the cluster and the Bee nodes. Results are
// cached briefly so several open tabs do not multiply the probes.
func (c *Controller) Status(ctx context.Context) Status {
	c.mu.Lock()
	if c.status != nil && time.Since(c.statusAt) < 1500*time.Millisecond {
		st := *c.status
		c.mu.Unlock()
		st.Job = c.currentOrLast()
		return st
	}
	c.mu.Unlock()

	st := Status{Enabled: true}
	st.Settings = c.Settings()
	st.Cluster.Name, st.Cluster.KubeContext, st.Cluster.Namespace = c.cluster, c.cfg.KubeContext, c.cfg.Namespace
	for _, name := range []string{"docker", "k3d", "kubectl", "helm", "beekeeper", "jq", "make"} {
		t := Tool{Name: name, Hint: toolHints[name]}
		if name == "beekeeper" && st.Settings.Beekeeper != "" {
			t.Path, t.OK = st.Settings.Beekeeper, isFile(st.Settings.Beekeeper)
		} else if p, err := exec.LookPath(name); err == nil {
			t.Path, t.OK = p, true
		}
		if t.OK {
			t.Hint = ""
		}
		st.Tools = append(st.Tools, t)
	}
	if _, err := quiet(ctx, 5*time.Second, "docker", "info", "--format", "{{.ServerVersion}}"); err == nil {
		st.Docker.Running = true
	} else {
		st.Docker.Error = "Docker is not running"
	}
	if st.Docker.Running {
		exists, running, err := c.k3dState(ctx)
		st.Cluster.Exists, st.Cluster.Running = exists, running
		if err != nil {
			st.Cluster.Error = err.Error()
		}
		if out, err := quiet(ctx, 5*time.Second, "docker", "inspect", "-f", "{{.State.Running}}", "k3d-registry.localhost"); err == nil {
			st.Cluster.RegistryRunning = out == "true"
		}
		if st.Cluster.RegistryRunning {
			st.Cluster.RegistryPort = c.registryPort(ctx)
			st.Cluster.LocalImage = c.LocalImage(ctx, "latest")
		}
	}
	st.Deployed.Groups = map[string]int{}
	if st.Cluster.Running {
		if cs, err := c.cfg.Kube.Clientset(); err == nil {
			c.kubeStatus(ctx, cs, &st)
		} else {
			st.Cluster.Error = err.Error()
		}
	}
	st.Profiles, st.Checks = c.profilesAndChecks()
	st.Hosts = missingHosts()

	c.mu.Lock()
	c.status, c.statusAt = &st, time.Now()
	c.mu.Unlock()
	st.Job = c.currentOrLast()
	return st
}

func (c *Controller) currentOrLast() *JobView {
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.current
	if j == nil && len(c.jobs) > 0 {
		j = c.jobs[len(c.jobs)-1]
	}
	if j == nil {
		return nil
	}
	v := j.View()
	return &v
}

type k3dCluster struct {
	Name           string `json:"name"`
	ServersCount   int    `json:"serversCount"`
	ServersRunning int    `json:"serversRunning"`
}

func (c *Controller) k3dState(ctx context.Context) (exists, running bool, err error) {
	out, err := quiet(ctx, 8*time.Second, "k3d", "cluster", "list", "-o", "json")
	if err != nil {
		return false, false, fmt.Errorf("k3d cluster list: %v", err)
	}
	var cl []k3dCluster
	if err := json.Unmarshal([]byte(out), &cl); err != nil {
		return false, false, fmt.Errorf("k3d cluster list: %v", err)
	}
	for _, k := range cl {
		if k.Name == c.cluster {
			return true, k.ServersRunning > 0, nil
		}
	}
	return false, false, nil
}

func (c *Controller) kubeStatus(ctx context.Context, cs *kubernetes.Clientset, st *Status) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ns := c.cfg.Namespace
	if g, err := cs.AppsV1().StatefulSets(ns).Get(ctx, "geth-swap", metav1.GetOptions{}); err == nil {
		st.Geth.Installed = true
		st.Geth.Ready = g.Status.ReadyReplicas > 0
	}
	sets, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{LabelSelector: beeSelector})
	if err != nil {
		st.Cluster.Error = err.Error()
		c.cfg.Kube.Reset()
		return
	}
	pods, _ := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: beeSelector})
	podOf := map[string]*corev1.Pod{}
	if pods != nil {
		for i := range pods.Items {
			p := &pods.Items[i]
			podOf[p.Labels["app.kubernetes.io/instance"]] = p
		}
	}
	images := map[string]int{}
	for _, s := range sets.Items {
		n := nodeStatus(&s, podOf[s.Name])
		st.Nodes = append(st.Nodes, n)
		st.Deployed.Groups[n.Group]++
		images[n.Image]++
	}
	sort.Slice(st.Nodes, func(i, j int) bool { return natLess(st.Nodes[i].Name, st.Nodes[j].Name) })
	best := 0
	for img, n := range images {
		if n > best || (n == best && img < st.Deployed.Image) {
			st.Deployed.Image, best = img, n
		}
	}
}

func nodeStatus(s *appsv1.StatefulSet, p *corev1.Pod) NodeStatus {
	n := NodeStatus{Name: s.Name, Group: groupOf(s.Name), Ready: s.Status.ReadyReplicas}
	if s.Spec.Replicas != nil {
		n.Replicas = *s.Spec.Replicas
	}
	if cs := s.Spec.Template.Spec.Containers; len(cs) > 0 {
		n.Image = cs[0].Image
	}
	switch {
	case n.Replicas == 0:
		n.Phase = "Stopped"
	case p == nil:
		n.Phase = "Missing"
	default:
		n.Pod, n.Phase = p.Name, string(p.Status.Phase)
		if p.DeletionTimestamp != nil {
			n.Phase = "Terminating"
		}
		for _, c := range p.Status.ContainerStatuses {
			n.Restarts += c.RestartCount
			if c.State.Waiting != nil && c.State.Waiting.Reason != "" {
				n.Reason = c.State.Waiting.Reason
			}
			if c.State.Running != nil {
				t := c.State.Running.StartedAt.Time
				n.StartedAt = &t
			}
		}
	}
	return n
}

const beeSelector = "app.kubernetes.io/name=bee"

func groupOf(name string) string {
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		return name[:i]
	}
	return name
}

func indexOf(name string) int {
	i := strings.LastIndexByte(name, '-')
	if i < 0 {
		return -1
	}
	n, err := strconv.Atoi(name[i+1:])
	if err != nil {
		return -1
	}
	return n
}

// natLess orders bee-2 before bee-10.
func natLess(a, b string) bool {
	ga, gb := groupOf(a), groupOf(b)
	if ga != gb {
		return ga < gb
	}
	return indexOf(a) < indexOf(b)
}

// hostNames are the /etc/hosts entries beelocal adds; beekeeper reaches
// geth through geth-swap.localhost.
var hostNames = []string{"geth-swap.localhost", "k3d-registry.localhost"}

func missingHosts() []string {
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return nil
	}
	var missing []string
	for _, h := range hostNames {
		if !hostsHas(string(b), h) {
			missing = append(missing, h)
		}
	}
	return missing
}

func hostsHas(hosts, name string) bool {
	for _, line := range strings.Split(hosts, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		for _, x := range f[min(1, len(f)):] {
			if x == name {
				return true
			}
		}
	}
	return false
}

func dockerPlatform() string { return "linux/" + runtime.GOARCH }
