package control

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Settings are the paths and deployment choices the Cluster tab edits. They
// persist in the user config dir.
type Settings struct {
	BeelocalDir        string          `json:"beelocalDir"`
	BeekeeperConfigDir string          `json:"beekeeperConfigDir"`
	BeeRepoDir         string          `json:"beeRepoDir"`
	Beekeeper          string          `json:"beekeeper"`
	Profile            string          `json:"profile"`
	Counts             map[string]int  `json:"counts"`
	Image              string          `json:"image"`
	Detected           map[string]bool `json:"detected"`
}

func (c *Controller) settingsPath() string { return filepath.Join(c.stateDir, "settings.json") }

func (c *Controller) loadSettings() {
	var s Settings
	if b, err := os.ReadFile(c.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	s.Detected = map[string]bool{}
	home, _ := os.UserHomeDir()
	bk := readBeekeeperYAML(home)
	if s.Beekeeper == "" {
		if p, err := exec.LookPath("beekeeper"); err == nil {
			s.Beekeeper, s.Detected["beekeeper"] = p, true
		}
	}
	if s.BeekeeperConfigDir == "" {
		if d := expandHome(bk.ConfigDir, home); d != "" && isDir(d) {
			s.BeekeeperConfigDir, s.Detected["beekeeperConfigDir"] = d, true
		}
	}
	roots := searchRoots(home, s.BeekeeperConfigDir)
	if s.BeelocalDir == "" {
		if d := findUp(roots, "beelocal", "beelocal.sh"); d != "" {
			s.BeelocalDir, s.Detected["beelocalDir"] = d, true
		}
	}
	if s.BeeRepoDir == "" {
		if d := findUp(roots, "bee", "Dockerfile.dev"); d != "" {
			s.BeeRepoDir, s.Detected["beeRepoDir"] = d, true
		}
	}
	c.walletKey = bk.WalletKey
	c.settings = s
}

func (c *Controller) saveSettings() error {
	if err := os.MkdirAll(c.stateDir, 0o700); err != nil {
		return err
	}
	s := c.settings
	s.Detected = nil
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.settingsPath(), b, 0o600)
}

// Settings returns a copy of the current settings.
func (c *Controller) Settings() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.copySettings()
}

func (c *Controller) copySettings() Settings {
	s := c.settings
	s.Counts = map[string]int{}
	for k, v := range c.settings.Counts {
		s.Counts[k] = v
	}
	s.Detected = map[string]bool{}
	for k, v := range c.settings.Detected {
		s.Detected[k] = v
	}
	return s
}

// UpdateSettings applies the non-empty fields of in and saves.
func (c *Controller) UpdateSettings(in Settings) (Settings, error) {
	for _, p := range []string{in.BeelocalDir, in.BeekeeperConfigDir, in.BeeRepoDir, in.Beekeeper} {
		if p != "" && !filepath.IsAbs(p) {
			return Settings{}, errors.New("paths must be absolute: " + p)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	set := func(dst *string, v, key string) {
		if v != "" && v != *dst {
			*dst = filepath.Clean(v)
			delete(c.settings.Detected, key)
		}
	}
	set(&c.settings.BeelocalDir, in.BeelocalDir, "beelocalDir")
	set(&c.settings.BeekeeperConfigDir, in.BeekeeperConfigDir, "beekeeperConfigDir")
	set(&c.settings.BeeRepoDir, in.BeeRepoDir, "beeRepoDir")
	set(&c.settings.Beekeeper, in.Beekeeper, "beekeeper")
	if in.Profile != "" {
		c.settings.Profile = in.Profile
	}
	if in.Image != "" {
		c.settings.Image = strings.TrimSpace(in.Image)
	}
	if in.Counts != nil {
		c.settings.Counts = in.Counts
	}
	c.profilesAt = c.profilesAt.Add(-staleAfter) // config dir may have changed
	return c.copySettings(), c.saveSettings()
}

type beekeeperYAML struct {
	ConfigDir string `yaml:"config-dir"`
	WalletKey string `yaml:"wallet-key"`
}

// readBeekeeperYAML reads ~/.beekeeper.yaml for the config dir and, only to
// scrub it from job output, the wallet key.
func readBeekeeperYAML(home string) beekeeperYAML {
	var y beekeeperYAML
	if b, err := os.ReadFile(filepath.Join(home, ".beekeeper.yaml")); err == nil {
		_ = yaml.Unmarshal(b, &y)
	}
	return y
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// searchRoots are directories whose ancestors may hold the beelocal and bee
// checkouts: the beekeeper config dir, the working dir, the executable's
// dir and the Go source tree.
func searchRoots(home, cfgDir string) []string {
	var roots []string
	if cfgDir != "" {
		roots = append(roots, cfgDir)
	}
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, wd)
	}
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	roots = append(roots, filepath.Join(home, "go", "src", "github.com", "ethersphere", "x"))
	return roots
}

// findUp looks for <ancestor>/<name>/<marker> walking up from each root.
func findUp(roots []string, name, marker string) string {
	for _, r := range roots {
		d := r
		for i := 0; i < 6; i++ {
			cand := filepath.Join(d, name)
			if isFile(filepath.Join(cand, marker)) {
				return cand
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	return ""
}
