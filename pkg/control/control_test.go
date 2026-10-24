package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const testConfig = `
clusters:
  local:
    _inherit: ""
    namespace: local
    funding:
      eth: 0.1
      bzz: 100.0
    node-groups:
      bootnode:
        mode: bootnode
        bee-config: bootnode-local
        config: local
        nodes:
          - name: bootnode-0
            bootnodes: /dns4/bootnode-0-headless.%s.svc.cluster.local/tcp/1634/p2p/Qm
      bee:
        bee-config: bee-local
        config: local
        count: 2
        mode: node
  local-dns:
    _inherit: local
    node-groups:
      bootnode:
        mode: bootnode
        config: local-dns
        nodes:
          - name: bootnode-0
      bee:
        config: local-dns
        bee-config: bee-local-dns
        count: 3
        mode: node
      light:
        config: local-light
        count: 2
        mode: node
  local-child:
    _inherit: local-dns
  remote:
    namespace: bee-playground
    node-groups:
      bee:
        count: 3
node-groups:
  local:
    image: k3d-registry.localhost:5000/ethersphere/bee:latest
  local-dns:
    _inherit: local
  local-light:
    _inherit: local
bee-configs:
  bee-local:
    swap-initial-deposit: 500000000000000000
  bee-local-dns:
    _inherit: bee-local
checks:
  ci-pushsync:
    options: {}
  pingpong:
    options: {}
`

func testController(t *testing.T) *Controller {
	t.Helper()
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "local.yaml"), []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Controller{cfg: Config{Namespace: "local"}, stateDir: t.TempDir(), settings: Settings{BeekeeperConfigDir: cfgDir}}
}

func TestProfiles(t *testing.T) {
	c := testController(t)
	profiles, checks := c.profilesAndChecks()
	var names []string
	for _, p := range profiles {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, ","); got != "local,local-child,local-dns" {
		t.Fatalf("profiles = %s (remote must be excluded, inherited node groups resolved)", got)
	}
	if strings.Join(checks, ",") != "ci-pushsync,pingpong" {
		t.Fatalf("checks = %v, want ci-* first", checks)
	}
	dns := profiles[2]
	if dns.Image != "k3d-registry.localhost:5000/ethersphere/bee:latest" {
		t.Fatalf("image = %q (resolved through node-group inheritance)", dns.Image)
	}
	want := map[string]int{"bootnode": 1, "bee": 3, "light": 2}
	for _, g := range dns.Groups {
		if want[g.Name] != g.Count {
			t.Fatalf("group %s count %d, want %d", g.Name, g.Count, want[g.Name])
		}
	}
	if dns.RequiredBZZ != 50 || dns.Funding.BZZ != 100 || dns.Funding.ETH != 0.1 {
		t.Fatalf("funding = %+v, required BZZ %v (swap-initial-deposit through bee-config inheritance)", dns.Funding, dns.RequiredBZZ)
	}
	if dns.Groups[0].Mode != "bootnode" {
		t.Fatalf("bootnode group must come first: %+v", dns.Groups)
	}
}

func TestWriteManagedConfig(t *testing.T) {
	c := testController(t)
	dir, groups, err := c.writeManagedConfig(Plan{Profile: "local-dns", Counts: map[string]int{"bee": 5, "light": 0}, Image: "ethersphere/bee:2.8.0"})
	if err != nil {
		t.Fatal(err)
	}
	if groups["bootnode"] != "bootnode" || groups["light"] != "node" {
		t.Fatalf("groups = %v", groups)
	}
	if _, err := os.Stat(filepath.Join(dir, "local.yaml")); err != nil {
		t.Fatal("the user's config files must be copied:", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "zz-bee-scope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Clusters map[string]struct {
			Inherit    string                    `yaml:"_inherit"`
			NodeGroups map[string]map[string]any `yaml:"node-groups"`
		} `yaml:"clusters"`
		NodeGroups map[string]map[string]any `yaml:"node-groups"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	cl := doc.Clusters[ManagedCluster]
	if cl.Inherit != "local-dns" {
		t.Fatalf("_inherit = %q", cl.Inherit)
	}
	if _, ok := cl.NodeGroups["light"]; ok {
		t.Fatal("a group with count 0 must be left out")
	}
	bee := cl.NodeGroups["bee"]
	if bee["count"] != 5 || bee["config"] != "bee-scope-local-dns" {
		t.Fatalf("bee group = %v", bee)
	}
	if _, ok := cl.NodeGroups["bootnode"]["nodes"]; !ok {
		t.Fatal("the bootnode keeps its explicit nodes (keys)")
	}
	ng := doc.NodeGroups["bee-scope-local-dns"]
	if ng["_inherit"] != "local-dns" || ng["image"] != "ethersphere/bee:2.8.0" {
		t.Fatalf("image override node group = %v", ng)
	}

	// A second deploy rewrites the dir from scratch.
	if _, _, err := c.writeManagedConfig(Plan{Profile: "local"}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "zz-bee-scope.yaml"))
	if strings.Contains(string(b), "light") || strings.Contains(string(b), "bee-scope-local-dns") {
		t.Fatalf("stale content after redeploy:\n%s", b)
	}

	if _, _, err := c.writeManagedConfig(Plan{Profile: "nope"}); err == nil {
		t.Fatal("unknown profile must fail")
	}
}

func TestScrubber(t *testing.T) {
	key := "4663c222787e30c1994b59044aa5045377a6e79193a8ead88293926b535c722d"
	c := &Controller{walletKey: key}
	s := c.scrubber()
	other := strings.Repeat("ab", 32)
	got := s("key=" + key + " 0x" + strings.ToUpper(key) + " other=" + other)
	if strings.Contains(strings.ToLower(got), key) {
		t.Fatalf("wallet key leaked: %s", got)
	}
	if !strings.Contains(got, other) {
		t.Fatalf("unrelated hex must stay: %s", got)
	}
}

func TestHelpers(t *testing.T) {
	hosts := "127.0.0.1 localhost\n# 127.0.0.1 geth-swap.localhost\n127.0.0.1\tk3d-registry.localhost bee-0.localhost\n"
	if hostsHas(hosts, "geth-swap.localhost") {
		t.Fatal("commented entries do not count")
	}
	if !hostsHas(hosts, "k3d-registry.localhost") {
		t.Fatal("tab-separated entry not found")
	}
	if groupOf("bootnode-0") != "bootnode" || indexOf("bee-12") != 12 || indexOf("geth") != -1 {
		t.Fatal("name helpers")
	}
	if !natLess("bee-2", "bee-10") || natLess("light-0", "bee-5") {
		t.Fatal("natLess")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "beelocal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "beelocal", "beelocal.sh"), []byte("declare -x REGISTRY_PORT=${REGISTRY_PORT:-5001}\ndeclare -x K3S_VERSION=${K3S_VERSION:-v1.35.6+k3s1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "DEV", "beekeeper", "config")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	found := findUp([]string{deep}, "beelocal", "beelocal.sh")
	if found != filepath.Join(root, "beelocal") {
		t.Fatalf("findUp = %q", found)
	}
	c := &Controller{settings: Settings{BeelocalDir: found}}
	if c.beelocalDefault("REGISTRY_PORT", "5000") != "5001" || c.beelocalDefault("K3S_VERSION", "") != "v1.35.6+k3s1" || c.beelocalDefault("NOPE", "x") != "x" {
		t.Fatal("beelocalDefault")
	}
}

func TestRunValidation(t *testing.T) {
	c := testController(t)
	for name, req := range map[string]ActionRequest{
		"node-restart": {Node: "bee-1; rm -rf /"},
		"image-build":  {Tag: "../x"},
		"check-run":    {Checks: []string{"ci-x --kubeconfig=/etc"}},
		"bee-deploy":   {Profile: "local", Image: "bad image"},
		"nope":         {},
	} {
		if _, err := c.Run(name, req); err == nil {
			t.Errorf("%s %+v: expected a validation error", name, req)
		}
	}
	if _, err := c.Run("check-run", ActionRequest{}); err == nil {
		t.Error("check-run without checks must fail")
	}
}

func TestSelectionCluster(t *testing.T) {
	c := testController(t)
	dir, _, err := c.writeManagedConfig(Plan{Profile: "local-dns", Counts: map[string]int{"bee": 4}, Only: []string{"bee-3", "bootnode-0"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "zz-bee-scope.yaml"))
	var doc struct {
		Clusters map[string]struct {
			NodeGroups map[string]struct {
				Count int              `yaml:"count"`
				Nodes []map[string]any `yaml:"nodes"`
			} `yaml:"node-groups"`
		} `yaml:"clusters"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	sel := doc.Clusters[SelectionCluster].NodeGroups
	if len(sel) != 2 || len(sel["bee"].Nodes) != 1 || sel["bee"].Nodes[0]["name"] != "bee-3" || sel["bee"].Count != 0 {
		t.Fatalf("selection = %+v", sel)
	}
	if _, ok := sel["light"]; ok {
		t.Fatal("groups without chosen nodes must be left out")
	}
	if len(sel["bootnode"].Nodes) != 1 || sel["bootnode"].Nodes[0]["name"] != "bootnode-0" {
		t.Fatalf("bootnode = %+v", sel["bootnode"])
	}
	if doc.Clusters[ManagedCluster].NodeGroups["bee"].Count != 4 {
		t.Fatal("the full cluster stays defined next to the selection")
	}
	if _, _, err := c.writeManagedConfig(Plan{Profile: "local-dns", Counts: map[string]int{"bee": 2}, Only: []string{"bee-7"}}); err == nil || !strings.Contains(err.Error(), "bee-7") {
		t.Fatalf("a node outside the layout must be named in the error: %v", err)
	}
}

func TestBeekeeperArgs(t *testing.T) {
	seed := int64(7)
	args, title, err := beekeeperArgs(ActionRequest{Command: "check", Checks: []string{"ci-pingpong"}, Nodes: []string{"bee-1"}, Seed: &seed, Timeout: "10m"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "check --checks ci-pingpong --metrics-enabled=false --timeout 10m0s --seed 7" {
		t.Fatalf("args = %s", got)
	}
	if title != "beekeeper check ci-pingpong · bee-1" {
		t.Fatalf("title = %s", title)
	}
	args, _, err = beekeeperArgs(ActionRequest{Command: "stamper-topup", BatchIDs: []string{strings.Repeat("a", 64)}})
	if err != nil || !strings.Contains(strings.Join(args, " "), "--ttl-threshold 120h0m0s --topup-to 720h0m0s") || args[len(args)-1] != strings.Repeat("a", 64) {
		t.Fatalf("topup = %v %v", args, err)
	}
	for _, bad := range []ActionRequest{
		{Command: "print", What: "secrets"},
		{Command: "check", Checks: []string{"x,--kubeconfig=/x"}},
		{Command: "stamper-create", Depth: 3},
		{Command: "stamper-create", Label: "a b"},
		{Command: "stamper-dilute", BatchIDs: []string{"zz"}},
		{Command: "node-funder"},
		{Command: "restart", Image: "x y"},
		{Command: "check", Checks: []string{"a"}, Nodes: []string{"bee-1;x"}},
		{Command: "check", Checks: []string{"a"}, Timeout: "-1s"},
		{Command: "rm"},
	} {
		if _, _, err := beekeeperArgs(bad); err == nil {
			t.Errorf("%+v: expected an error", bad)
		}
	}
}

func TestParseRaw(t *testing.T) {
	args, err := parseRaw(`$ beekeeper node-funder --min-native 0.1 --min-swarm 100 --timeout 15m --cluster-name bee-scope`)
	if err != nil || strings.Join(args, "|") != "node-funder|--min-native|0.1|--min-swarm|100|--timeout|15m|--cluster-name|bee-scope" {
		t.Fatalf("args = %q %v", args, err)
	}
	args, err = parseRaw(`check --checks ci-pingpong --seed 3 --metrics-labels "team=swarm x"`)
	if err != nil || args[len(args)-1] != "--metrics-enabled=false" || args[6] != "team=swarm x" {
		t.Fatalf("check args = %q %v", args, err)
	}
	if v, ok := flagValue([]string{"print", "--cluster-name=local"}, "--cluster-name"); !ok || v != "local" {
		t.Fatal("flagValue =form")
	}
	for _, bad := range []string{
		"", "beekeeper", "rm -rf /", "beekeeper operator", `check --kubeconfig /tmp/x`, `check --config-dir=/etc`, `check --in-cluster`,
		`print 'unterminated`, "check --checks a\x00b",
	} {
		if _, err := parseRaw(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestClusterFor(t *testing.T) {
	c := testController(t)
	if n, err := c.clusterFor(ActionRequest{}, []string{"print"}); err != nil || n != ManagedCluster {
		t.Fatal(n, err)
	}
	if n, err := c.clusterFor(ActionRequest{Nodes: []string{"bee-1"}}, []string{"print"}); err != nil || n != SelectionCluster {
		t.Fatal(n, err)
	}
	if n, err := c.clusterFor(ActionRequest{}, []string{"print", "--cluster-name", "local-dns"}); err != nil || n != "local-dns" {
		t.Fatal(n, err)
	}
	for _, req := range []ActionRequest{{Cluster: "remote"}, {Cluster: SelectionCluster}, {Cluster: "nope"}} {
		if _, err := c.clusterFor(req, []string{"print"}); err == nil {
			t.Errorf("%+v: expected an error", req)
		}
	}
}
