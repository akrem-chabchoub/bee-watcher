package control

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ManagedCluster is the beekeeper cluster bee-scope generates and deploys.
const ManagedCluster = "bee-scope"

// SelectionCluster is ManagedCluster narrowed to the nodes chosen for one
// beekeeper command, so commands can target single nodes.
const SelectionCluster = "bee-scope-selection"

const staleAfter = 10 * time.Second

// Profile is a beekeeper cluster definition that targets the local
// namespace.
type Profile struct {
	Name   string         `json:"name"`
	Groups []ProfileGroup `json:"groups"`
	Image  string         `json:"image"`
	// Funding is what beekeeper sends each node when it creates the
	// cluster (the profile's funding: eth, bzz).
	Funding struct {
		ETH float64 `json:"eth"`
		BZZ float64 `json:"bzz"`
	} `json:"funding"`
	// RequiredBZZ is the largest swap-initial-deposit of the profile's
	// node groups, in BZZ: a node with less cannot deploy its chequebook
	// and does not start.
	RequiredBZZ float64 `json:"requiredBzz"`
}

// ProfileGroup is one node group of a profile.
type ProfileGroup struct {
	Name  string `json:"name"`
	Mode  string `json:"mode"`
	Count int    `json:"count"`
}

// bkConfig is the merged content of a beekeeper config dir, kept as plain
// maps so unknown fields survive into the generated file.
type bkConfig struct {
	clusters    map[string]map[string]any
	nodeGroups  map[string]map[string]any
	beeConfigs  map[string]map[string]any
	checks      []string
	simulations []string
}

func readBKConfig(dir string) (*bkConfig, error) {
	files, err := yamlFiles(dir)
	if err != nil {
		return nil, err
	}
	bc := &bkConfig{clusters: map[string]map[string]any{}, nodeGroups: map[string]map[string]any{}, beeConfigs: map[string]map[string]any{}}
	seenCheck := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		for k, v := range asMap(doc["clusters"]) {
			if _, dup := bc.clusters[k]; !dup {
				bc.clusters[k] = asMap(v)
			}
		}
		for k, v := range asMap(doc["node-groups"]) {
			if _, dup := bc.nodeGroups[k]; !dup {
				bc.nodeGroups[k] = asMap(v)
			}
		}
		for k, v := range asMap(doc["bee-configs"]) {
			if _, dup := bc.beeConfigs[k]; !dup {
				bc.beeConfigs[k] = asMap(v)
			}
		}
		for k := range asMap(doc["checks"]) {
			if !seenCheck[k] {
				seenCheck[k] = true
				bc.checks = append(bc.checks, k)
			}
		}
		for k := range asMap(doc["simulations"]) {
			if !seenCheck["sim:"+k] {
				seenCheck["sim:"+k] = true
				bc.simulations = append(bc.simulations, k)
			}
		}
	}
	sort.Slice(bc.checks, func(i, j int) bool {
		ci, cj := strings.HasPrefix(bc.checks[i], "ci-"), strings.HasPrefix(bc.checks[j], "ci-")
		if ci != cj {
			return ci
		}
		return bc.checks[i] < bc.checks[j]
	})
	sort.Strings(bc.simulations)
	return bc, nil
}

func yamlFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && (strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml")) {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Strings(out)
	return out, nil
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// lookup resolves a field through the _inherit chain.
func lookup(set map[string]map[string]any, name, field string) any {
	for i := 0; i < 16 && name != ""; i++ {
		m, ok := set[name]
		if !ok {
			return nil
		}
		if v, ok := m[field]; ok && v != nil {
			return v
		}
		name, _ = m["_inherit"].(string)
	}
	return nil
}

func (bc *bkConfig) profile(name string) (Profile, bool) {
	ns, _ := lookup(bc.clusters, name, "namespace").(string)
	static, _ := lookup(bc.clusters, name, "use-static-endpoints").(bool)
	groups := asMap(lookup(bc.clusters, name, "node-groups"))
	if static || len(groups) == 0 {
		return Profile{}, false
	}
	p := Profile{Name: name}
	_ = ns
	for gname, gv := range groups {
		g := asMap(gv)
		mode, _ := g["mode"].(string)
		cnt := toInt(g["count"])
		if nodes, ok := g["nodes"].([]any); ok && len(nodes) > 0 {
			cnt = len(nodes)
		}
		p.Groups = append(p.Groups, ProfileGroup{Name: gname, Mode: mode, Count: cnt})
		if cfg, _ := g["config"].(string); cfg != "" && p.Image == "" {
			p.Image, _ = lookup(bc.nodeGroups, cfg, "image").(string)
		}
		if bcName, _ := g["bee-config"].(string); bcName != "" {
			if dep := plurToBZZ(lookup(bc.beeConfigs, bcName, "swap-initial-deposit")); dep > p.RequiredBZZ {
				p.RequiredBZZ = dep
			}
		}
	}
	f := asMap(lookup(bc.clusters, name, "funding"))
	p.Funding.ETH, p.Funding.BZZ = toFloat(f["eth"]), toFloat(f["bzz"])
	sort.Slice(p.Groups, func(i, j int) bool {
		if (p.Groups[i].Mode == "bootnode") != (p.Groups[j].Mode == "bootnode") {
			return p.Groups[i].Mode == "bootnode"
		}
		return p.Groups[i].Name < p.Groups[j].Name
	})
	return p, true
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

// plurToBZZ converts a PLUR amount (1 BZZ = 1e16 PLUR) to BZZ.
func plurToBZZ(v any) float64 {
	if s, ok := v.(string); ok {
		if b, ok := new(big.Float).SetString(s); ok {
			f, _ := new(big.Float).Quo(b, big.NewFloat(1e16)).Float64()
			return f
		}
		return 0
	}
	return toFloat(v) / 1e16
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// profilesAndChecks lists the local profiles (clusters in the config dir
// whose namespace is the watched one) and the defined checks, cached.
func (c *Controller) profilesAndChecks() ([]Profile, []string) {
	c.mu.Lock()
	if time.Since(c.profilesAt) < staleAfter && c.profiles != nil {
		p, ch := c.profiles, c.checks
		c.mu.Unlock()
		return p, ch
	}
	dir := c.settings.BeekeeperConfigDir
	c.mu.Unlock()
	profiles, checks := []Profile{}, []string{}
	if bc, err := readBKConfig(dir); err == nil {
		checks = bc.checks
		c.mu.Lock()
		c.simulations = bc.simulations
		c.mu.Unlock()
		names := make([]string, 0, len(bc.clusters))
		for n := range bc.clusters {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if ns, _ := lookup(bc.clusters, n, "namespace").(string); ns != c.ns() || n == ManagedCluster {
				continue
			}
			if p, ok := bc.profile(n); ok {
				profiles = append(profiles, p)
			}
		}
	}
	c.mu.Lock()
	c.profiles, c.checks, c.profilesAt = profiles, checks, time.Now()
	c.mu.Unlock()
	return profiles, checks
}

// Plan is what a deploy generates.
type Plan struct {
	Profile string
	Counts  map[string]int // non-bootnode groups
	Image   string
	// Only, when set, also defines SelectionCluster with just these nodes.
	Only []string
}

// writeManagedConfig copies the user's beekeeper config dir into the state
// dir and adds the bee-scope cluster: the profile's node groups with the
// requested counts and, optionally, a different image.
func (c *Controller) writeManagedConfig(pl Plan) (dir string, groups map[string]string, err error) {
	src := c.Settings().BeekeeperConfigDir
	if src == "" {
		return "", nil, fmt.Errorf("beekeeper config dir is not set (Settings)")
	}
	bc, err := readBKConfig(src)
	if err != nil {
		return "", nil, fmt.Errorf("read beekeeper config: %w", err)
	}
	if _, ok := bc.clusters[pl.Profile]; !ok {
		return "", nil, fmt.Errorf("profile %q is not defined in %s", pl.Profile, src)
	}
	dir = filepath.Join(c.stateDir, "beekeeper-config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	old, _ := yamlFiles(dir)
	for _, f := range old {
		_ = os.Remove(f)
	}
	files, err := yamlFiles(src)
	if err != nil {
		return "", nil, err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o600); err != nil {
			return "", nil, err
		}
	}

	groups = map[string]string{} // group -> mode
	ngs := map[string]any{}
	extraNG := map[string]any{}
	for gname, gv := range asMap(lookup(bc.clusters, pl.Profile, "node-groups")) {
		g := map[string]any{}
		for k, v := range asMap(gv) {
			g[k] = v
		}
		mode, _ := g["mode"].(string)
		groups[gname] = mode
		if mode != "bootnode" {
			n, ok := pl.Counts[gname]
			if !ok {
				n = toInt(g["count"])
				if nodes, ok := g["nodes"].([]any); ok && len(nodes) > 0 {
					n = len(nodes)
				}
			}
			if n <= 0 {
				continue
			}
			g["count"] = n
			delete(g, "nodes")
			delete(g, "endpoints")
		}
		if cfg, _ := g["config"].(string); cfg != "" && pl.Image != "" {
			name := ManagedCluster + "-" + cfg
			extraNG[name] = map[string]any{"_inherit": cfg, "image": pl.Image}
			g["config"] = name
		}
		ngs[gname] = g
	}
	clusters := map[string]any{ManagedCluster: map[string]any{"_inherit": pl.Profile, "node-groups": ngs}}
	if len(pl.Only) > 0 {
		sel, err := selectNodes(ngs, pl.Only)
		if err != nil {
			return "", nil, err
		}
		clusters[SelectionCluster] = map[string]any{"_inherit": pl.Profile, "node-groups": sel}
	}
	doc := map[string]any{"clusters": clusters}
	if len(extraNG) > 0 {
		doc["node-groups"] = extraNG
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", nil, err
	}
	hdr := "# Generated by bee-scope from profile " + pl.Profile + ". Do not edit; it is rewritten on every deploy.\n"
	if err := os.WriteFile(filepath.Join(dir, "zz-bee-scope.yaml"), append([]byte(hdr), b...), 0o600); err != nil {
		return "", nil, err
	}
	return dir, groups, nil
}

// selectNodes narrows node groups to the named nodes: bootnode groups keep
// their matching explicit entries (with keys), other groups list the nodes
// by name instead of a count.
func selectNodes(ngs map[string]any, only []string) (map[string]any, error) {
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	out := map[string]any{}
	for gname, gv := range ngs {
		g := asMap(gv)
		var keep []any
		if nodes, ok := g["nodes"].([]any); ok && len(nodes) > 0 {
			for i, nv := range nodes {
				name, _ := asMap(nv)["name"].(string)
				if name == "" {
					name = fmt.Sprintf("%s-%d", gname, i)
				}
				if want[name] {
					keep = append(keep, nv)
					delete(want, name)
				}
			}
		} else {
			for i := 0; i < toInt(g["count"]); i++ {
				name := fmt.Sprintf("%s-%d", gname, i)
				if want[name] {
					keep = append(keep, map[string]any{"name": name})
					delete(want, name)
				}
			}
		}
		if len(keep) == 0 {
			continue
		}
		ng := map[string]any{}
		for k, v := range g {
			ng[k] = v
		}
		delete(ng, "count")
		delete(ng, "endpoints")
		ng["nodes"] = keep
		out[gname] = ng
	}
	if len(want) > 0 {
		var missing []string
		for n := range want {
			missing = append(missing, n)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("not part of the deployed layout: %s (deploy from the Cluster tab first)", strings.Join(missing, ", "))
	}
	return out, nil
}
