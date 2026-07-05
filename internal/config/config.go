// Package config resolves shipyard's on-disk locations and settings.
package config

import (
	"encoding/json"
	"fmt"
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

// Settings is the optional user config at <ConfigDir>/config.json.
type Settings struct {
	// LeadAllowedTools are extra tool allowlist entries for lead agents
	// (e.g. "mcp__linear" to let leads read Linear tickets). Leads always
	// get mcp__shipyard; crewmates are unrestricted in their worktrees.
	LeadAllowedTools []string `json:"lead_allowed_tools"`
}

func (p Paths) SettingsPath() string { return filepath.Join(p.ConfigDir, "config.json") }

// LoadSettings reads config.json; a missing file returns zero settings.
func (p Paths) LoadSettings() (Settings, error) {
	var s Settings
	b, err := os.ReadFile(p.SettingsPath())
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", p.SettingsPath(), err)
	}
	return s, nil
}

func (p Paths) DBPath() string      { return filepath.Join(p.DataDir, "shipyard.db") }
func (p Paths) SocketPath() string  { return filepath.Join(p.DataDir, "daemon.sock") }
func (p Paths) PIDPath() string     { return filepath.Join(p.DataDir, "daemon.pid") }
func (p Paths) LogDir() string      { return filepath.Join(p.DataDir, "logs") }
func (p Paths) ChannelsDir() string { return filepath.Join(p.DataDir, "channels") }
func (p Paths) RunbooksDir() string { return filepath.Join(p.DataDir, "runbooks") }
func (p Paths) ReposDir() string    { return filepath.Join(p.DataDir, "repos") } // clones made by channel setup

// ChannelDir is the human-readable data dir for one channel: instructions.md,
// tasks/<id>/{brief.md,report.md}.
func (p Paths) ChannelDir(name string) string {
	return filepath.Join(p.ChannelsDir(), name)
}

func (p Paths) TaskDir(channel, taskID string) string {
	return filepath.Join(p.ChannelDir(channel), "tasks", taskID)
}
