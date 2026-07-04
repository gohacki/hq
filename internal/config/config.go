// Package config resolves shipyard's on-disk locations and settings.
package config

import (
	"os"
	"path/filepath"
)

// Paths holds every filesystem location shipyard uses. All state lives under
// DataDir so the whole installation can be inspected or wiped in one place.
type Paths struct {
	ConfigDir string // ~/.config/shipyard
	DataDir   string // ~/.local/share/shipyard
}

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	p := Paths{
		ConfigDir: filepath.Join(home, ".config", "shipyard"),
		DataDir:   filepath.Join(home, ".local", "share", "shipyard"),
	}
	if v := os.Getenv("SHIPYARD_CONFIG_DIR"); v != "" {
		p.ConfigDir = v
	}
	if v := os.Getenv("SHIPYARD_DATA_DIR"); v != "" {
		p.DataDir = v
	}
	return p, nil
}

func (p Paths) Ensure() error {
	for _, d := range []string{p.ConfigDir, p.DataDir, p.ChannelsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (p Paths) DBPath() string       { return filepath.Join(p.DataDir, "shipyard.db") }
func (p Paths) SocketPath() string   { return filepath.Join(p.DataDir, "daemon.sock") }
func (p Paths) PIDPath() string      { return filepath.Join(p.DataDir, "daemon.pid") }
func (p Paths) LogDir() string       { return filepath.Join(p.DataDir, "logs") }
func (p Paths) ChannelsDir() string  { return filepath.Join(p.DataDir, "channels") }
func (p Paths) ReposDir() string     { return filepath.Join(p.DataDir, "repos") } // clones made by channel setup

// ChannelDir is the human-readable data dir for one channel: instructions.md,
// tasks/<id>/{brief.md,report.md}.
func (p Paths) ChannelDir(name string) string {
	return filepath.Join(p.ChannelsDir(), name)
}

func (p Paths) TaskDir(channel, taskID string) string {
	return filepath.Join(p.ChannelDir(channel), "tasks", taskID)
}
