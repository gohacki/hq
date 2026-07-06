// Package config resolves hq's on-disk locations.
package config

import (
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
