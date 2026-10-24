package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ActionRequest is the body of POST /api/control/actions/{name}.
type ActionRequest struct {
	Confirm bool           `json:"confirm"`
	Node    string         `json:"node"`
	Profile string         `json:"profile"`
	Counts  map[string]int `json:"counts"`
	Image   string         `json:"image"`
	Tag     string         `json:"tag"`
	Checks  []string       `json:"checks"`
	Timeout string         `json:"timeout"`

	// beekeeper action (Beekeeper panel); Nodes empty = whole cluster.
	Command        string   `json:"command"`
	Nodes          []string `json:"nodes"`
	Simulations    []string `json:"simulations"`
	What           string   `json:"what"` // print: addresses, depths, nodes, overlays, peers, topologies
	Seed           *int64   `json:"seed"`
	Depth          int      `json:"depth"`
	Duration       string   `json:"duration"`
	Label          string   `json:"label"`
	TTLThreshold   string   `json:"ttlThreshold"`
	TopupTo        string   `json:"topupTo"`
	UsageThreshold float64  `json:"usageThreshold"`
	DilutionDepth  int      `json:"dilutionDepth"`
	BatchIDs       []string `json:"batchIds"`
	MinNative      float64  `json:"minNative"`
	MinSwarm       float64  `json:"minSwarm"`
	// Cluster is the beekeeper --cluster-name (default: bee-scope, or
	// bee-scope-selection when Nodes is set).
	Cluster string `json:"cluster"`
	// Raw is a hand-edited command line; it replaces Command and the typed
	// fields. See parseRaw for what is allowed.
	Raw string `json:"raw"`
}

// Actions lists the action names.
var Actions = []string{"docker-start", "cluster-create", "cluster-start", "cluster-stop", "cluster-delete", "bee-deploy", "bee-remove", "node-restart", "node-stop", "node-start", "restart-all", "image-build", "check-run", "beekeeper"}

var (
	nodeName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,50}[a-z0-9])?$`)
	imageRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,250}$`)
	tagName  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	checkRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// Run validates and starts an action as a job.
func (c *Controller) Run(name string, req ActionRequest) (*Job, error) {
	switch name {
	case "docker-start":
		return c.start(name, "Start Docker", []string{"Open Docker Desktop", "Wait for the Docker engine"}, c.dockerStart)
	case "cluster-create":
		return c.start(name, "Create the local k3d cluster", []string{
			"Check prerequisites", "Local image registry", "k3d cluster", "Helm chart repository", "Wait for Traefik", "Install geth (local blockchain)",
		}, c.clusterCreate)
	case "cluster-start":
		return c.start(name, "Start the cluster", []string{"Start the image registry", "Start k3d cluster " + c.k3d(), "Wait for the Kubernetes API"}, c.clusterStart)
	case "cluster-stop":
		return c.start(name, "Stop the cluster", []string{"Stop k3d cluster " + c.k3d(), "Stop the image registry"}, c.clusterStop)
	case "cluster-delete":
		return c.start(name, "Delete the cluster", []string{"Delete k3d cluster " + c.k3d(), "Delete the image registries"}, c.clusterDelete)
	case "bee-deploy":
		pl, title, err := c.plan(req)
		if err != nil {
			return nil, err
		}
		return c.start(name, title, []string{
			"Check the cluster", "Install geth if missing", "Generate the beekeeper config", "beekeeper create bee-cluster", "Remove nodes above the requested counts", "Wait for the nodes",
		}, func(ctx context.Context, j *Job) error { return c.beeDeploy(ctx, j, pl) })
	case "bee-remove":
		return c.start(name, "Remove all Bee nodes", []string{"Delete Bee resources"}, func(ctx context.Context, j *Job) error {
			return j.step(ctx, 0, func(ctx context.Context) error { return c.deleteByLabel(ctx, j, beeSelector) })
		})
	case "node-restart", "node-stop", "node-start":
		if !nodeName.MatchString(req.Node) {
			return nil, errors.New("invalid node name")
		}
		verb := map[string]string{"node-restart": "Restart", "node-stop": "Stop", "node-start": "Start"}[name]
		return c.start(name, verb+" "+req.Node, []string{verb + " " + req.Node, "Wait for " + req.Node}, func(ctx context.Context, j *Job) error {
			return c.nodeOp(ctx, j, name, req.Node)
		})
	case "restart-all":
		return c.start(name, "Restart all Bee nodes", []string{"Delete every Bee pod", "Wait for the nodes"}, c.restartAll)
	case "image-build":
		tag := req.Tag
		if tag == "" {
			tag = "latest"
		}
		if !tagName.MatchString(tag) {
			return nil, errors.New("invalid image tag")
		}
		return c.start(name, "Build Bee image :"+tag+" from the local repo", []string{"Check the bee repo and registry", "make docker-build", "Push to the local registry"}, func(ctx context.Context, j *Job) error {
			return c.imageBuild(ctx, j, tag)
		})
	case "check-run":
		// Kept for API compatibility: the same as beekeeper with command check.
		req.Command = "check"
		fallthrough
	case "beekeeper":
		for _, n := range req.Nodes {
			if !nodeName.MatchString(n) {
				return nil, fmt.Errorf("invalid node name %q", n)
			}
		}
		var args []string
		var title string
		var err error
		if strings.TrimSpace(req.Raw) != "" {
			args, err = parseRaw(req.Raw)
			title = "beekeeper " + strings.Join(args, " ")
			if len(title) > 120 {
				title = title[:117] + "…"
			}
		} else {
			args, title, err = beekeeperArgs(req)
			if err == nil && req.Command == "node-funder" {
				if need := c.requiredBZZ(); req.MinSwarm > 0 && req.MinSwarm < need {
					err = fmt.Errorf("min BZZ %g is below the %g BZZ each node needs to deploy its chequebook (swap-initial-deposit); nodes would stay stuck", req.MinSwarm, need)
				}
			}
		}
		if err != nil {
			return nil, err
		}
		cluster, err := c.clusterFor(req, args)
		if err != nil {
			return nil, err
		}
		nodes := append([]string(nil), req.Nodes...)
		return c.start("beekeeper", title, []string{"Generate the beekeeper config", "beekeeper " + args[0]}, func(ctx context.Context, j *Job) error {
			return c.runBeekeeper(ctx, j, nodes, args, cluster)
		})
	}
	return nil, fmt.Errorf("unknown action %q", name)
}

func (c *Controller) plan(req ActionRequest) (Plan, string, error) {
	s := c.Settings()
	pl := Plan{Profile: req.Profile, Counts: req.Counts, Image: strings.TrimSpace(req.Image)}
	if pl.Profile == "" {
		pl.Profile = s.Profile
	}
	if pl.Profile == "" {
		return pl, "", errors.New("choose a beekeeper profile")
	}
	if pl.Counts == nil {
		pl.Counts = s.Counts
	}
	if pl.Image != "" && !imageRef.MatchString(pl.Image) {
		return pl, "", errors.New("invalid image reference")
	}
	profiles, _ := c.profilesAndChecks()
	var prof *Profile
	for i := range profiles {
		if profiles[i].Name == pl.Profile {
			prof = &profiles[i]
		}
	}
	if prof == nil {
		return pl, "", fmt.Errorf("profile %q not found in the beekeeper config dir", pl.Profile)
	}
	parts := []string{}
	for _, g := range prof.Groups {
		n := g.Count
		if g.Mode != "bootnode" {
			if v, ok := pl.Counts[g.Name]; ok {
				n = v
			}
			if n < 0 || n > 30 {
				return pl, "", fmt.Errorf("%s count must be 0–30", g.Name)
			}
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, g.Name))
	}
	// Remember the choice for the next deploy and for checks.
	_, _ = c.UpdateSettings(Settings{Profile: pl.Profile, Counts: pl.Counts, Image: pl.Image})
	return pl, "Deploy " + strings.Join(parts, ", "), nil
}

func (c *Controller) dockerRunning(ctx context.Context) bool {
	_, err := quiet(ctx, 5*time.Second, "docker", "info", "--format", "{{.ServerVersion}}")
	return err == nil
}

func (c *Controller) dockerStart(ctx context.Context, j *Job) error {
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		if c.dockerRunning(ctx) {
			return j.skipf("Docker is already running")
		}
		if runtime.GOOS != "darwin" {
			return errors.New("start the Docker daemon yourself (only macOS can be started from here)")
		}
		return j.run(ctx, "", "open", "-a", "Docker")
	}); err != nil {
		return err
	}
	return j.step(ctx, 1, func(ctx context.Context) error {
		return j.waitFor(ctx, "the Docker engine", 3*time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
			return c.dockerRunning(ctx), nil
		})
	})
}

// beelocalDefault reads `declare -x NAME=${NAME:-value}` from beelocal.sh,
// so cluster creation follows the checked-out beelocal version.
func (c *Controller) beelocalDefault(name, fallback string) string {
	f, err := os.Open(filepath.Join(c.Settings().BeelocalDir, "beelocal.sh"))
	if err != nil {
		return fallback
	}
	defer f.Close()
	re := regexp.MustCompile(`^declare -x ` + regexp.QuoteMeta(name) + `=\$\{` + regexp.QuoteMeta(name) + `:-([^}]*)\}`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := re.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil {
			return strings.Trim(m[1], `"`)
		}
	}
	return fallback
}

func (c *Controller) kubectl(args ...string) []string {
	return append([]string{"--context", c.kctx()}, args...)
}

func (c *Controller) clusterCreate(ctx context.Context, j *Job) error {
	s := c.Settings()
	port := c.beelocalDefault("REGISTRY_PORT", "5000")
	k3sVersion := c.beelocalDefault("K3S_VERSION", "")
	ns := c.ns()
	steps := []func(ctx context.Context) error{
		func(ctx context.Context) error {
			if !c.dockerRunning(ctx) {
				return errors.New("Docker is not running: start it first")
			}
			for _, t := range []string{"k3d", "kubectl", "helm"} {
				if _, err := exec.LookPath(t); err != nil {
					return fmt.Errorf("%s is not installed (%s)", t, toolHints[t])
				}
			}
			if s.BeelocalDir == "" || !isFile(filepath.Join(s.BeelocalDir, "config", "k3d.yaml")) {
				return errors.New("beelocal checkout not found: set its path in Settings")
			}
			if miss := missingHosts(); len(miss) > 0 {
				j.logf("err", "  /etc/hosts is missing %s; beekeeper needs geth-swap.localhost. Add it once with:", strings.Join(miss, ", "))
				j.logf("err", "  echo '127.0.0.1 %s' | sudo tee -a /etc/hosts", strings.Join(hostNames, " "))
			}
			j.logf("info", "  beelocal: %s (k3s %s, registry port %s)", s.BeelocalDir, k3sVersion, port)
			return nil
		},
		func(ctx context.Context) error {
			if out, err := quiet(ctx, 5*time.Second, "docker", "inspect", "-f", "{{.State.Running}}", "k3d-registry.localhost"); err == nil {
				if out != "true" {
					return j.run(ctx, "", "docker", "start", "k3d-registry.localhost")
				}
				return j.skipf("registry k3d-registry.localhost exists")
			}
			return j.run(ctx, "", "k3d", "registry", "create", "registry.localhost", "-p", port)
		},
		func(ctx context.Context) error {
			exists, running, err := c.k3dState(ctx)
			if err != nil {
				return err
			}
			if exists && running {
				return j.skipf("cluster %s is already running", c.k3d())
			}
			if exists {
				return j.run(ctx, "", "k3d", "cluster", "start", c.k3d())
			}
			raw, err := os.ReadFile(filepath.Join(s.BeelocalDir, "config", "k3d.yaml"))
			if err != nil {
				return err
			}
			tmp := filepath.Join(c.stateDir, "k3d.yaml")
			if err := os.MkdirAll(c.stateDir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(tmp, []byte(strings.ReplaceAll(string(raw), "${REGISTRY_PORT}", port)), 0o600); err != nil {
				return err
			}
			defer os.Remove(tmp)
			args := []string{"cluster", "create", "--config", tmp, "--kubeconfig-switch-context=false"}
			if k3sVersion != "" {
				args = append(args, "--image", "rancher/k3s:"+strings.ReplaceAll(k3sVersion, "+", "-"))
			}
			if err := j.run(ctx, "", "k3d", args...); err != nil {
				return err
			}
			c.cfg.Kube.Reset()
			return nil
		},
		func(ctx context.Context) error {
			if out, _ := quiet(ctx, 10*time.Second, "helm", "repo", "list"); !strings.Contains(out, "ethersphere") {
				if err := j.run(ctx, "", "helm", "repo", "add", "ethersphere", "https://ethersphere.github.io/helm"); err != nil {
					return err
				}
			}
			return j.run(ctx, "", "helm", "repo", "update", "ethersphere")
		},
		func(ctx context.Context) error {
			if err := j.waitFor(ctx, "the Traefik IngressRoute CRD", 5*time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
				_, err1 := quiet(ctx, 10*time.Second, "kubectl", c.kubectl("get", "crd", "ingressroutes.traefik.io")...)
				_, err2 := quiet(ctx, 10*time.Second, "kubectl", c.kubectl("get", "crd", "ingressroutes.traefik.containo.us")...)
				return err1 == nil || err2 == nil, nil
			}); err != nil {
				return err
			}
			return j.waitFor(ctx, "the Traefik service", 5*time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
				_, err := quiet(ctx, 10*time.Second, "kubectl", c.kubectl("get", "svc", "traefik", "-n", "kube-system")...)
				return err == nil, nil
			})
		},
		func(ctx context.Context) error { return c.installGeth(ctx, j, ns) },
	}
	for i, fn := range steps {
		if err := j.step(ctx, i, fn); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) installGeth(ctx context.Context, j *Job, ns string) error {
	if _, err := quiet(ctx, 15*time.Second, "helm", "--kube-context", c.kctx(), "get", "values", "geth-swap", "-n", ns, "-o", "json"); err == nil {
		return j.skipf("geth-swap is already installed")
	}
	s := c.Settings()
	values := filepath.Join(s.BeelocalDir, "config", "geth-swap.yaml")
	if !isFile(values) {
		return errors.New("beelocal config/geth-swap.yaml not found: set the beelocal path in Settings")
	}
	img := c.beelocalDefault("SETUP_CONTRACT_IMAGE", "ethersphere/bee-localchain")
	tag := c.beelocalDefault("SETUP_CONTRACT_IMAGE_TAG", "latest")
	if err := j.run(ctx, "", "helm", "--kube-context", c.kctx(), "install", "geth-swap", "ethersphere/geth-swap",
		"--create-namespace", "-n", ns, "-f", values,
		"--set", "imageSetupContract.repository="+img, "--set", "imageSetupContract.tag="+tag); err != nil {
		return err
	}
	return j.waitFor(ctx, "the geth contract setup job", 10*time.Minute, 3*time.Second, func(ctx context.Context) (bool, error) {
		out, err := quiet(ctx, 10*time.Second, "kubectl", c.kubectl("get", "pod", "-n", ns, "-l", "job-name=geth-swap-setupcontracts",
			"-o", "jsonpath={.items[-1:].status.containerStatuses[0].state.terminated.reason}")...)
		return out == "Completed", err
	})
}

func (c *Controller) clusterStart(ctx context.Context, j *Job) error {
	if !c.dockerRunning(ctx) {
		return errors.New("Docker is not running: start it first")
	}
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		return j.run(ctx, "", "docker", "start", "k3d-registry.localhost")
	}); err != nil {
		return err
	}
	if err := j.step(ctx, 1, func(ctx context.Context) error {
		return j.run(ctx, "", "k3d", "cluster", "start", c.k3d())
	}); err != nil {
		return err
	}
	c.cfg.Kube.Reset()
	return j.step(ctx, 2, func(ctx context.Context) error {
		return j.waitFor(ctx, "the Kubernetes API", 2*time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
			_, err := quiet(ctx, 8*time.Second, "kubectl", c.kubectl("get", "ns", c.ns())...)
			return err == nil, err
		})
	})
}

func (c *Controller) clusterStop(ctx context.Context, j *Job) error {
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		return j.run(ctx, "", "k3d", "cluster", "stop", c.k3d())
	}); err != nil {
		return err
	}
	return j.step(ctx, 1, func(ctx context.Context) error {
		return j.run(ctx, "", "docker", "stop", "k3d-registry.localhost")
	})
}

func (c *Controller) clusterDelete(ctx context.Context, j *Job) error {
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		return j.run(ctx, "", "k3d", "cluster", "delete", c.k3d())
	}); err != nil {
		return err
	}
	c.cfg.Kube.Reset()
	return j.step(ctx, 1, func(ctx context.Context) error {
		for _, r := range []string{"k3d-registry.localhost", "k3d-" + c.k3d() + "-registry"} {
			if _, err := quiet(ctx, 5*time.Second, "docker", "inspect", r); err == nil {
				if err := j.run(ctx, "", "k3d", "registry", "delete", r); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// kubeconfigFile writes a single-context kubeconfig for beekeeper, so it
// can only ever reach the local cluster.
func (c *Controller) kubeconfigFile(ctx context.Context) (string, func(), error) {
	out, err := quiet(ctx, 15*time.Second, "k3d", "kubeconfig", "get", c.k3d())
	if err != nil {
		return "", nil, fmt.Errorf("k3d kubeconfig get %s: %v", c.k3d(), err)
	}
	if err := os.MkdirAll(c.stateDir, 0o700); err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(c.stateDir, "kubeconfig-*.yaml")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	_, werr := f.WriteString(out + "\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(name)
		return "", nil, errors.Join(werr, cerr)
	}
	return name, func() { os.Remove(name) }, nil
}

func (c *Controller) beekeeperBin() (string, error) {
	if p := c.Settings().Beekeeper; p != "" && isFile(p) {
		return p, nil
	}
	if p, err := exec.LookPath("beekeeper"); err == nil {
		return p, nil
	}
	return "", errors.New("beekeeper not found: install it or set its path in Settings")
}

func (c *Controller) beeDeploy(ctx context.Context, j *Job, pl Plan) error {
	var cs *kubernetes.Clientset
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		_, running, err := c.k3dState(ctx)
		if err != nil {
			return err
		}
		if !running {
			return errors.New("the k3d cluster is not running: create or start it first")
		}
		cs, err = c.cfg.Kube.Clientset()
		return err
	}); err != nil {
		return err
	}
	if err := j.step(ctx, 1, func(ctx context.Context) error { return c.installGeth(ctx, j, c.ns()) }); err != nil {
		return err
	}
	var dir string
	var groups map[string]string
	if err := j.step(ctx, 2, func(ctx context.Context) error {
		var err error
		dir, groups, err = c.writeManagedConfig(pl)
		if err == nil {
			j.logf("info", "  %s/zz-bee-scope.yaml (cluster %q from profile %q)", dir, ManagedCluster, pl.Profile)
		}
		return err
	}); err != nil {
		return err
	}
	if err := j.step(ctx, 3, func(ctx context.Context) error {
		bin, err := c.beekeeperBin()
		if err != nil {
			return err
		}
		kc, cleanup, err := c.kubeconfigFile(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
		return j.run(ctx, "", bin, "create", "bee-cluster", "--cluster-name", ManagedCluster, "--config-dir", dir, "--kubeconfig", kc, "--timeout", "30m")
	}); err != nil {
		return err
	}
	if err := j.step(ctx, 4, func(ctx context.Context) error {
		sets, err := cs.AppsV1().StatefulSets(c.ns()).List(ctx, metav1.ListOptions{LabelSelector: beeSelector})
		if err != nil {
			return err
		}
		var extra []string
		for _, s := range sets.Items {
			g := groupOf(s.Name)
			mode, inProfile := groups[g]
			if mode == "bootnode" {
				continue
			}
			want, ok := pl.Counts[g]
			if !inProfile || (ok && indexOf(s.Name) >= want) {
				extra = append(extra, s.Name)
			}
		}
		if len(extra) == 0 {
			return j.skipf("no extra nodes")
		}
		sort.Strings(extra)
		for _, n := range extra {
			if err := c.deleteByLabel(ctx, j, "app.kubernetes.io/instance="+n); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return j.step(ctx, 5, func(ctx context.Context) error { return c.waitNodesReady(ctx, j, cs, 10*time.Minute) })
}

func (c *Controller) waitNodesReady(ctx context.Context, j *Job, cs *kubernetes.Clientset, timeout time.Duration) error {
	return j.waitFor(ctx, "all Bee nodes to be ready", timeout, 3*time.Second, func(ctx context.Context) (bool, error) {
		sets, err := cs.AppsV1().StatefulSets(c.ns()).List(ctx, metav1.ListOptions{LabelSelector: beeSelector})
		if err != nil {
			return false, err
		}
		var notReady []string
		for _, s := range sets.Items {
			want := int32(1)
			if s.Spec.Replicas != nil {
				want = *s.Spec.Replicas
			}
			if s.Status.ReadyReplicas < want {
				notReady = append(notReady, s.Name)
			}
		}
		if len(notReady) > 0 {
			sort.Strings(notReady)
			return false, fmt.Errorf("not ready: %s", strings.Join(notReady, ", "))
		}
		return true, nil
	})
}

// deleteByLabel removes every namespaced resource beekeeper creates for Bee
// nodes that match the selector. geth is not labeled app.kubernetes.io/name=bee.
func (c *Controller) deleteByLabel(ctx context.Context, j *Job, selector string) error {
	kinds := "statefulset,service,secret,configmap,persistentvolumeclaim,serviceaccount"
	if _, err := quiet(ctx, 10*time.Second, "kubectl", c.kubectl("get", "crd", "ingressroutes.traefik.io")...); err == nil {
		kinds += ",ingressroutes.traefik.io"
	}
	return j.run(ctx, "", "kubectl", c.kubectl("delete", kinds, "-n", c.ns(), "-l", selector, "--ignore-not-found", "--wait=false")...)
}

func (c *Controller) nodeOp(ctx context.Context, j *Job, op, node string) error {
	cs, err := c.cfg.Kube.Clientset()
	if err != nil {
		return err
	}
	ns := c.ns()
	set, err := cs.AppsV1().StatefulSets(ns).Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("node %s: %w", node, err)
	}
	if set.Labels["app.kubernetes.io/name"] != "bee" {
		return fmt.Errorf("%s is not a Bee node", node)
	}
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		switch op {
		case "node-restart":
			return j.run(ctx, "", "kubectl", c.kubectl("delete", "pod", "-n", ns, "-l", "app.kubernetes.io/instance="+node, "--wait=false")...)
		case "node-stop":
			return j.run(ctx, "", "kubectl", c.kubectl("scale", "statefulset", node, "-n", ns, "--replicas=0")...)
		default:
			return j.run(ctx, "", "kubectl", c.kubectl("scale", "statefulset", node, "-n", ns, "--replicas=1")...)
		}
	}); err != nil {
		return err
	}
	return j.step(ctx, 1, func(ctx context.Context) error {
		if op == "node-stop" {
			return j.waitFor(ctx, node+" to stop", 3*time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
				pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + node})
				return err == nil && len(pods.Items) == 0, err
			})
		}
		started := time.Now()
		return j.waitFor(ctx, node+" to be ready", 5*time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
			s, err := cs.AppsV1().StatefulSets(ns).Get(ctx, node, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			// After a restart the old pod reports ready until it is gone.
			if op == "node-restart" && time.Since(started) < 5*time.Second {
				return false, nil
			}
			return s.Status.ReadyReplicas >= 1, nil
		})
	})
}

func (c *Controller) restartAll(ctx context.Context, j *Job) error {
	cs, err := c.cfg.Kube.Clientset()
	if err != nil {
		return err
	}
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		return j.run(ctx, "", "kubectl", c.kubectl("delete", "pod", "-n", c.ns(), "-l", beeSelector, "--wait=false")...)
	}); err != nil {
		return err
	}
	return j.step(ctx, 1, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
		return c.waitNodesReady(ctx, j, cs, 10*time.Minute)
	})
}

// registryPort is the host port of the local registry. beelocal maps it
// to REGISTRY_PORT, which is also the port the cluster's registry mirror is
// configured for, so it is used both to push and in image names.
func (c *Controller) registryPort(ctx context.Context) string {
	if out, err := quiet(ctx, 5*time.Second, "docker", "port", "k3d-registry.localhost", "5000/tcp"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if i := strings.LastIndexByte(line, ':'); i >= 0 && i < len(line)-1 {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return c.beelocalDefault("REGISTRY_PORT", "5000")
}

// LocalImage is where a locally built image is pulled from inside the
// cluster.
func (c *Controller) LocalImage(ctx context.Context, tag string) string {
	return "k3d-registry.localhost:" + c.registryPort(ctx) + "/ethersphere/bee:" + tag
}

func (c *Controller) imageBuild(ctx context.Context, j *Job, tag string) error {
	s := c.Settings()
	port := c.registryPort(ctx)
	push := "localhost:" + port + "/ethersphere/bee:" + tag
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		if s.BeeRepoDir == "" || !isFile(filepath.Join(s.BeeRepoDir, "Dockerfile.dev")) {
			return errors.New("bee repo not found: set its path in Settings")
		}
		if !c.dockerRunning(ctx) {
			return errors.New("Docker is not running")
		}
		if out, err := quiet(ctx, 5*time.Second, "docker", "inspect", "-f", "{{.State.Running}}", "k3d-registry.localhost"); err != nil || out != "true" {
			return errors.New("the local registry k3d-registry.localhost is not running: start the cluster first")
		}
		j.logf("info", "  repo %s · platform %s", s.BeeRepoDir, dockerPlatform())
		return nil
	}); err != nil {
		return err
	}
	if err := j.step(ctx, 1, func(ctx context.Context) error {
		return j.run(ctx, s.BeeRepoDir, "make", "docker-build", "BEE_IMAGE="+push, "PLATFORM="+dockerPlatform())
	}); err != nil {
		return err
	}
	return j.step(ctx, 2, func(ctx context.Context) error {
		if err := j.run(ctx, "", "docker", "push", push); err != nil {
			return err
		}
		j.logf("info", "  nodes can now use image %s", c.LocalImage(ctx, tag))
		return nil
	})
}

// BeekeeperCommands are the commands the Beekeeper panel runs.
var BeekeeperCommands = []string{"check", "simulate", "print", "stamper-create", "stamper-topup", "stamper-dilute", "node-funder", "restart"}

var (
	printWhat = map[string]bool{"addresses": true, "depths": true, "nodes": true, "overlays": true, "peers": true, "topologies": true}
	batchID   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	labelText = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

func parseDur(s, def string, max time.Duration) (string, error) {
	if s == "" {
		s = def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 || d > max {
		return "", fmt.Errorf("invalid duration %q (e.g. 15m, 24h; at most %s)", s, max)
	}
	return d.String(), nil
}

// beekeeperArgs validates a Beekeeper panel request and returns the command
// arguments (without cluster, config and kubeconfig flags) and a job title.
// Every value is typed and checked: nothing is passed through verbatim.
func beekeeperArgs(req ActionRequest) ([]string, string, error) {
	for _, n := range req.Nodes {
		if !nodeName.MatchString(n) {
			return nil, "", fmt.Errorf("invalid node name %q", n)
		}
	}
	scope := "whole cluster"
	if len(req.Nodes) > 0 {
		scope = strings.Join(req.Nodes, ", ")
	}
	timeout, err := parseDur(req.Timeout, "15m", 3*time.Hour)
	if err != nil {
		return nil, "", err
	}
	seed := func(args []string) []string {
		if req.Seed != nil {
			args = append(args, "--seed", strconv.FormatInt(*req.Seed, 10))
		}
		return args
	}
	var args []string
	var what string
	switch req.Command {
	case "check":
		if len(req.Checks) == 0 {
			return nil, "", errors.New("choose at least one check")
		}
		for _, ch := range req.Checks {
			if !checkRe.MatchString(ch) {
				return nil, "", fmt.Errorf("invalid check name %q", ch)
			}
		}
		args = seed([]string{"check", "--checks", strings.Join(req.Checks, ","), "--metrics-enabled=false", "--timeout", timeout})
		what = "check " + strings.Join(req.Checks, ", ")
	case "simulate":
		if len(req.Simulations) == 0 {
			return nil, "", errors.New("choose at least one simulation")
		}
		for _, sm := range req.Simulations {
			if !checkRe.MatchString(sm) {
				return nil, "", fmt.Errorf("invalid simulation name %q", sm)
			}
		}
		args = seed([]string{"simulate", "--simulations", strings.Join(req.Simulations, ","), "--metrics-enabled=false", "--timeout", timeout})
		what = "simulate " + strings.Join(req.Simulations, ", ")
	case "print":
		if !printWhat[req.What] {
			return nil, "", errors.New("print needs one of: addresses, depths, nodes, overlays, peers, topologies")
		}
		args = []string{"print", req.What, "--timeout", timeout}
		what = "print " + req.What
	case "stamper-create":
		depth := req.Depth
		if depth == 0 {
			depth = 17
		}
		if depth < 17 || depth > 40 {
			return nil, "", errors.New("depth must be 17–40")
		}
		dur, err := parseDur(req.Duration, "24h", 365*24*time.Hour)
		if err != nil {
			return nil, "", err
		}
		label := req.Label
		if label == "" {
			label = "beekeeper"
		}
		if !labelText.MatchString(label) {
			return nil, "", errors.New("label: letters, digits, _ . - only")
		}
		args = []string{"stamper", "create", "--depth", strconv.Itoa(depth), "--duration", dur, "--postage-label", label, "--timeout", timeout}
		what = fmt.Sprintf("stamper create depth %d for %s", depth, dur)
	case "stamper-topup":
		ttl, err := parseDur(req.TTLThreshold, "120h", 365*24*time.Hour)
		if err != nil {
			return nil, "", err
		}
		to, err := parseDur(req.TopupTo, "720h", 3*365*24*time.Hour)
		if err != nil {
			return nil, "", err
		}
		args = []string{"stamper", "topup", "--ttl-threshold", ttl, "--topup-to", to, "--timeout", timeout}
		what = "stamper topup to " + to
	case "stamper-dilute":
		usage := req.UsageThreshold
		if usage == 0 {
			usage = 90
		}
		levels := req.DilutionDepth
		if levels == 0 {
			levels = 1
		}
		if usage <= 0 || usage > 100 || levels < 1 || levels > 10 {
			return nil, "", errors.New("usage threshold must be 0–100 % and dilution depth 1–10")
		}
		args = []string{"stamper", "dilute", "--usage-threshold", strconv.FormatFloat(usage, 'f', -1, 64), "--dilution-depth", strconv.Itoa(levels), "--timeout", timeout}
		what = fmt.Sprintf("stamper dilute +%d above %g%%", levels, usage)
	case "node-funder":
		if req.MinNative < 0 || req.MinSwarm < 0 || req.MinNative > 1000 || req.MinSwarm > 1e6 || (req.MinNative == 0 && req.MinSwarm == 0) {
			return nil, "", errors.New("set a minimum ETH and/or BZZ balance (positive)")
		}
		args = []string{"node-funder", "--min-native", strconv.FormatFloat(req.MinNative, 'f', -1, 64), "--min-swarm", strconv.FormatFloat(req.MinSwarm, 'f', -1, 64), "--timeout", timeout}
		what = "node-funder"
	case "restart":
		args = []string{"restart", "--timeout", timeout}
		if img := strings.TrimSpace(req.Image); img != "" {
			if !imageRef.MatchString(img) {
				return nil, "", errors.New("invalid image reference")
			}
			args = append(args, "--image", img)
		}
		what = "restart"
	default:
		return nil, "", fmt.Errorf("unknown beekeeper command %q", req.Command)
	}
	if strings.HasPrefix(req.Command, "stamper-") && len(req.BatchIDs) > 0 && req.Command != "stamper-create" {
		for _, id := range req.BatchIDs {
			if !batchID.MatchString(id) {
				return nil, "", fmt.Errorf("invalid batch id %q", id)
			}
		}
		args = append(args, "--batch-ids", strings.Join(req.BatchIDs, ","))
	}
	return args, "beekeeper " + what + " · " + scope, nil
}

// runBeekeeper runs a beekeeper command against the deployed layout, or
// against just the chosen nodes.
func (c *Controller) runBeekeeper(ctx context.Context, j *Job, nodes []string, args []string, cluster string) error {
	s := c.Settings()
	if s.Profile == "" {
		return errors.New("bee-scope does not know the cluster layout yet: deploy once from the Cluster tab (or choose a profile there)")
	}
	var dir string
	if err := j.step(ctx, 0, func(ctx context.Context) error {
		var err error
		dir, _, err = c.writeManagedConfig(Plan{Profile: s.Profile, Counts: s.Counts, Image: s.Image, Only: nodes})
		if err == nil {
			j.logf("info", "  cluster %q from profile %q", cluster, s.Profile)
		}
		return err
	}); err != nil {
		return err
	}
	return j.step(ctx, 1, func(ctx context.Context) error {
		bin, err := c.beekeeperBin()
		if err != nil {
			return err
		}
		kc, cleanup, err := c.kubeconfigFile(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
		full := append([]string{}, args...)
		if _, has := flagValue(args, "--cluster-name"); !has && !namespaced(args) {
			full = append(full, "--cluster-name", cluster)
		}
		full = append(full, "--config-dir", dir, "--kubeconfig", kc)
		return j.run(ctx, "", bin, full...)
	})
}

// DefaultsFromDeployment picks a profile and counts that match what is
// deployed, when the user has not chosen any yet.
func (c *Controller) DefaultsFromDeployment(ctx context.Context) {
	if c.Settings().Profile != "" {
		return
	}
	st := c.Status(ctx)
	if len(st.Deployed.Groups) == 0 {
		return
	}
	best, bestScore := "", -1
	for _, p := range st.Profiles {
		score := 0
		for _, g := range p.Groups {
			if n, ok := st.Deployed.Groups[g.Name]; ok {
				score += 10
				if n == g.Count {
					score++
				}
			} else {
				score -= 10
			}
		}
		if len(p.Groups) == len(st.Deployed.Groups) {
			score += 5
		}
		if score > bestScore {
			best, bestScore = p.Name, score
		}
	}
	if best == "" {
		return
	}
	counts := map[string]int{}
	for g, n := range st.Deployed.Groups {
		if g != "bootnode" {
			counts[g] = n
		}
	}
	_, _ = c.UpdateSettings(Settings{Profile: best, Counts: counts, Image: st.Deployed.Image})
	b, _ := json.Marshal(counts)
	c.cfg.Log.Info("cluster control: matched the deployed nodes", "profile", best, "counts", string(b))
}

// requiredBZZ is the swap-initial-deposit (in BZZ) of the chosen profile.
func (c *Controller) requiredBZZ() float64 {
	prof := c.Settings().Profile
	profiles, _ := c.profilesAndChecks()
	for _, p := range profiles {
		if p.Name == prof {
			return p.RequiredBZZ
		}
	}
	return 0
}

// clusterFor picks and checks the beekeeper --cluster-name: the managed
// cluster, the node selection, or a profile in the config dir that targets
// the watched namespace.
func (c *Controller) clusterFor(req ActionRequest, args []string) (string, error) {
	name := req.Cluster
	if v, ok := flagValue(args, "--cluster-name"); ok {
		name = v
	}
	if name == "" {
		if len(req.Nodes) > 0 {
			return SelectionCluster, nil
		}
		return ManagedCluster, nil
	}
	switch name {
	case ManagedCluster:
		return name, nil
	case SelectionCluster:
		if len(req.Nodes) == 0 {
			return "", errors.New("bee-scope-selection needs chosen nodes (Scope: Chosen nodes)")
		}
		return name, nil
	}
	profiles, _ := c.profilesAndChecks()
	for _, p := range profiles {
		if p.Name == name {
			return name, nil
		}
	}
	return "", fmt.Errorf("cluster %q is not a beekeeper cluster for namespace %q in the config dir", name, c.ns())
}

// flagValue finds --name value or --name=value.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(a, name+"=") {
			return a[len(name)+1:], true
		}
	}
	return "", false
}

// namespaced reports commands told to work on a namespace instead of a
// cluster (beekeeper ignores --cluster-name then).
func namespaced(args []string) bool {
	for _, a := range args {
		if a == "-n" || a == "--namespace" || strings.HasPrefix(a, "--namespace=") || strings.HasPrefix(a, "--addresses") || a == "-a" {
			return true
		}
	}
	return false
}

// rawSubcommands are the beekeeper commands a hand-edited line may run.
var rawSubcommands = map[string]bool{
	"check": true, "simulate": true, "print": true, "stamper": true, "node-funder": true, "fund": true,
	"restart": true, "nuke": true, "create": true, "delete": true, "version": true,
}

// rawForbidden are flags that would point beekeeper at another cluster or
// config; bee-scope supplies them itself.
var rawForbidden = []string{"--kubeconfig", "--in-cluster", "--config", "--config-dir", "--config-git-repo", "--config-git-branch",
	"--config-git-dir", "--config-git-username", "--config-git-password", "--enable-k8s"}

// parseRaw splits a hand-edited command line (shell-style quoting, no
// shell is involved) and checks it: an optional leading "$" and
// "beekeeper", an allowed subcommand, no forbidden flags. check and
// simulate default to --metrics-enabled=false so nothing is pushed to the
// staging pushgateway.
func parseRaw(line string) ([]string, error) {
	if len(line) > 4000 {
		return nil, errors.New("command line too long")
	}
	args, err := splitArgs(line)
	if err != nil {
		return nil, err
	}
	if len(args) > 0 && args[0] == "$" {
		args = args[1:]
	}
	if len(args) > 0 && (args[0] == "beekeeper" || strings.HasSuffix(args[0], "/beekeeper")) {
		args = args[1:]
	}
	if len(args) == 0 {
		return nil, errors.New("empty command: e.g. beekeeper check --checks ci-pingpong")
	}
	if len(args) > 80 {
		return nil, errors.New("too many arguments")
	}
	if !rawSubcommands[args[0]] {
		return nil, fmt.Errorf("unsupported beekeeper command %q (allowed: check, simulate, print, stamper, node-funder, fund, restart, nuke, create, delete, version)", args[0])
	}
	for _, a := range args {
		for _, f := range rawForbidden {
			if a == f || strings.HasPrefix(a, f+"=") {
				return nil, fmt.Errorf("%s is set by bee-scope (it keeps beekeeper on the selected cluster); remove it", f)
			}
		}
		if strings.ContainsAny(a, "\x00\n\r") {
			return nil, errors.New("control characters are not allowed")
		}
	}
	if args[0] == "check" || args[0] == "simulate" {
		if _, ok := flagValue(args, "--metrics-enabled"); !ok && !contains(args, "--metrics-enabled") {
			args = append(args, "--metrics-enabled=false")
		}
	}
	return args, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// splitArgs splits like a POSIX shell: whitespace separates, single quotes
// are literal, double quotes allow \" and \\, backslash escapes outside
// quotes.
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				esc = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			esc, in = true, true
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 || esc {
		return nil, errors.New("unterminated quote or escape")
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}
