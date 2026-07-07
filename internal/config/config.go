// Package config resolves hq's on-disk locations and user configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Paths holds every filesystem location hq uses. All state lives under
// DataDir so the whole installation can be inspected or wiped in one place.
type Paths struct {
	ConfigDir string // ~/.config/hq
	DataDir   string // ~/.local/share/hq
}

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	p := Paths{
		ConfigDir: filepath.Join(home, ".config", "hq"),
		DataDir:   filepath.Join(home, ".local", "share", "hq"),
	}
	if v := os.Getenv("HQ_CONFIG_DIR"); v != "" {
		p.ConfigDir = v
	}
	if v := os.Getenv("HQ_DATA_DIR"); v != "" {
		p.DataDir = v
	}
	return p, nil
}

func (p Paths) Ensure() error {
	for _, d := range []string{p.ConfigDir, p.DataDir, p.ProjectsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (p Paths) DBPath() string        { return filepath.Join(p.DataDir, "hq.db") }
func (p Paths) SocketPath() string    { return filepath.Join(p.DataDir, "daemon.sock") }
func (p Paths) PIDPath() string       { return filepath.Join(p.DataDir, "daemon.pid") }
func (p Paths) LogDir() string        { return filepath.Join(p.DataDir, "logs") }
func (p Paths) ProjectsDir() string   { return filepath.Join(p.DataDir, "projects") }
func (p Paths) OnboardingDir() string { return filepath.Join(p.DataDir, "onboarding") } // per-repo cached onboarding docs

// ProjectDir is the human-readable data dir for one project: handbook.md,
// plans, tickets/<id>/{brief.md,report.md}.
func (p Paths) ProjectDir(name string) string {
	return filepath.Join(p.ProjectsDir(), name)
}

func (p Paths) TicketDir(project, ticketID string) string {
	return filepath.Join(p.ProjectDir(project), "tickets", ticketID)
}

// WorktreesDir holds every ticket's worktree set: worktrees/<ticket>/<repo>.
func (p Paths) WorktreesDir() string { return filepath.Join(p.DataDir, "worktrees") }

// PiSessionsDir holds the pi harness's session files (the director's and
// EMs' durable memory when pi is the manager harness).
func (p Paths) PiSessionsDir() string { return filepath.Join(p.DataDir, "pi-sessions") }

// ConfigPath is hq's user configuration file.
func (p Paths) ConfigPath() string { return filepath.Join(p.ConfigDir, "config.json") }

// Config is hq's user configuration (<config>/config.json). Every field is
// optional; zero values mean the defaults below.
type Config struct {
	// EMHarness runs the director and project EMs: "claude" (default) or "pi".
	EMHarness string `json:"em_harness"`
	// EngHarness runs engineers: "claude" (default) or "pi".
	EngHarness string `json:"eng_harness"`
}

// LoadConfig reads config.json, applies defaults, and honors the
// HQ_EM_HARNESS / HQ_ENG_HARNESS env overrides. A missing file is fine; a
// malformed one is an error (silently ignoring it would mask typos).
func (p Paths) LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(p.ConfigPath())
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return c, err
	default:
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parsing %s: %w", p.ConfigPath(), err)
		}
	}
	if v := os.Getenv("HQ_EM_HARNESS"); v != "" {
		c.EMHarness = v
	}
	if v := os.Getenv("HQ_ENG_HARNESS"); v != "" {
		c.EngHarness = v
	}
	if c.EMHarness == "" {
		c.EMHarness = "claude"
	}
	if c.EngHarness == "" {
		c.EngHarness = "claude"
	}
	for _, h := range []string{c.EMHarness, c.EngHarness} {
		if h != "claude" && h != "pi" {
			return c, fmt.Errorf("unknown harness %q in %s (want claude or pi)", h, p.ConfigPath())
		}
	}
	return c, nil
}
