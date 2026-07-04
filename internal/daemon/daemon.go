// Package daemon is shipyard's long-lived core: sole owner of the store,
// spawner/supervisor of agent processes, and RPC server for TUI clients.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gohacki/shipyard/internal/config"
	"github.com/gohacki/shipyard/internal/rpc"
	"github.com/gohacki/shipyard/internal/store"
)

// Orchestrator is the judgment layer: it decides how captain messages reach
// agents. The daemon core stays deterministic and testable behind it.
type Orchestrator interface {
	// CaptainChannelMessage handles a captain message in a channel's main
	// scroll (routes to the channel's lead agent, or the home assistant).
	CaptainChannelMessage(ctx context.Context, ch store.Channel, body string)
	// CaptainThreadMessage handles a captain reply inside a task thread
	// (steers the crewmate).
	CaptainThreadMessage(ctx context.Context, t store.Task, body string)
	// Reconcile is called at boot with all non-terminal tasks.
	Reconcile(ctx context.Context, tasks []store.Task)
}

type Daemon struct {
	Paths  config.Paths
	Store  *store.Store
	Server *rpc.Server
	Orch   Orchestrator
	Log    *slog.Logger
}

func New(paths config.Paths, st *store.Store, log *slog.Logger) *Daemon {
	d := &Daemon{
		Paths:  paths,
		Store:  st,
		Server: rpc.NewServer(paths.SocketPath()),
		Log:    log,
	}
	d.registerHandlers()
	return d
}

func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Server.Listen(); err != nil {
		return err
	}
	if err := os.WriteFile(d.Paths.PIDPath(), []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		return err
	}
	defer os.Remove(d.Paths.PIDPath())
	if err := d.ensureHomeChannel(); err != nil {
		return err
	}
	if d.Orch != nil {
		active, err := d.Store.ActiveTasks()
		if err != nil {
			return err
		}
		d.Orch.Reconcile(ctx, active)
	}
	d.Log.Info("daemon listening", "socket", d.Paths.SocketPath())
	return d.Server.Serve(ctx)
}

// HomeChannelName is the built-in channel where the home assistant lives
// (channel creation wizard, cross-channel questions).
const HomeChannelName = "home"

func (d *Daemon) ensureHomeChannel() error {
	_, err := d.Store.ChannelByName(HomeChannelName)
	if err == nil {
		return nil
	}
	if err != store.ErrNotFound {
		return err
	}
	_, err = d.createChannel(HomeChannelName, nil, "local-only")
	return err
}

// PostMessage appends a message and publishes it to subscribers. It is the
// single choke point every message (captain, lead, crew, system) goes through.
func (d *Daemon) PostMessage(m store.Message) (store.Message, error) {
	id, err := d.Store.AppendMessage(m)
	if err != nil {
		return m, err
	}
	m.ID = id
	d.Server.Publish(rpc.EvMessageNew, m)
	return m, nil
}

// UpdateTask persists a task change and publishes it.
func (d *Daemon) UpdateTask(t store.Task) error {
	if err := d.Store.UpdateTask(t); err != nil {
		return err
	}
	d.Server.Publish(rpc.EvTaskUpdated, t)
	return nil
}

// NotifyNeedsInput records that a thread needs the captain and publishes the
// signal (TUI turns it into badges + bell + desktop notification).
func (d *Daemon) NotifyNeedsInput(n rpc.NeedsInput) {
	d.Server.Publish(rpc.EvNeedsInput, n)
}

// --- channel creation ---

var channelNameOK = func(name string) bool {
	if name == "" || len(name) > 40 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (d *Daemon) createChannel(name string, repoPaths []string, delivery string) (store.Channel, error) {
	if !channelNameOK(name) {
		return store.Channel{}, fmt.Errorf("invalid channel name %q (lowercase, digits, - _)", name)
	}
	if delivery == "" {
		delivery = "no-mistakes"
	}
	switch delivery {
	case "no-mistakes", "direct-pr", "local-only":
	default:
		return store.Channel{}, fmt.Errorf("invalid delivery mode %q", delivery)
	}
	dir := d.Paths.ChannelDir(name)
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		return store.Channel{}, err
	}
	instructions := filepath.Join(dir, "instructions.md")
	if _, err := os.Stat(instructions); os.IsNotExist(err) {
		seed := fmt.Sprintf("# #%s — channel instructions\n\nConventions, goals, and constraints for this project. Injected into the lead's and every crewmate's context.\n", name)
		if err := os.WriteFile(instructions, []byte(seed), 0o644); err != nil {
			return store.Channel{}, err
		}
	}
	ch := store.Channel{
		ID:               store.NewID("ch"),
		Name:             name,
		Delivery:         delivery,
		InstructionsPath: instructions,
	}
	if err := d.Store.CreateChannel(ch); err != nil {
		return store.Channel{}, err
	}
	for _, rp := range repoPaths {
		if _, err := d.addRepo(ch, rp); err != nil {
			return store.Channel{}, fmt.Errorf("repo %s: %w", rp, err)
		}
	}
	d.Server.Publish(rpc.EvChannelCreated, ch)
	return ch, nil
}

// addRepo registers a local repo with a channel and makes sure it has a
// treehouse pool config so crewmate worktrees can be leased from it.
func (d *Daemon) addRepo(ch store.Channel, path string) (store.Repo, error) {
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return store.Repo{}, err
	}
	if fi, err := os.Stat(filepath.Join(abs, ".git")); err != nil || !fi.IsDir() {
		return store.Repo{}, fmt.Errorf("%s is not a git repository", abs)
	}
	branch := gitDefaultBranch(abs)
	r := store.Repo{
		ID:            store.NewID("repo"),
		ChannelID:     ch.ID,
		Name:          filepath.Base(abs),
		Path:          abs,
		DefaultBranch: branch,
	}
	if err := ensureTreehouseConfig(abs); err != nil {
		return store.Repo{}, err
	}
	if err := d.Store.AddRepo(r); err != nil {
		return store.Repo{}, err
	}
	return r, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func gitDefaultBranch(repo string) string {
	out, err := exec.Command("git", "-C", repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err == nil {
		return strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/")
	}
	out, err = exec.Command("git", "-C", repo, "symbolic-ref", "--short", "HEAD").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return "main"
}

func ensureTreehouseConfig(repo string) error {
	p := filepath.Join(repo, "treehouse.toml")
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	// Minimal pool config; worktrees land under $HOME/.treehouse by default.
	return os.WriteFile(p, []byte("max_trees = 16\nroot = \"\"\n"), 0o644)
}

// --- RPC handlers ---

func unmarshal[T any](raw json.RawMessage) (T, error) {
	var v T
	if raw == nil {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

// ChannelView is a channel plus derived UI state.
type ChannelView struct {
	store.Channel
	Unread int          `json:"unread"`
	Repos  []store.Repo `json:"repos"`
}

// TaskView is a task plus derived UI state.
type TaskView struct {
	store.Task
	Unread int `json:"unread"`
}

func (d *Daemon) registerHandlers() {
	d.Server.Handle("ping", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return "pong", nil
	})

	d.Server.Handle("channels.list", func(ctx context.Context, _ json.RawMessage) (any, error) {
		chs, err := d.Store.Channels()
		if err != nil {
			return nil, err
		}
		out := make([]ChannelView, 0, len(chs))
		for _, c := range chs {
			unread, err := d.Store.UnreadCount(c.ID, "")
			if err != nil {
				return nil, err
			}
			repos, err := d.Store.ReposForChannel(c.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, ChannelView{Channel: c, Unread: unread, Repos: repos})
		}
		return out, nil
	})

	d.Server.Handle("channels.create", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			Name     string   `json:"name"`
			Repos    []string `json:"repos"`
			Delivery string   `json:"delivery"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return d.createChannel(p.Name, p.Repos, p.Delivery)
	})

	d.Server.Handle("messages.list", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ChannelID string `json:"channel_id"`
			TaskID    string `json:"task_id"`
			Limit     int    `json:"limit"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return d.Store.Messages(p.ChannelID, p.TaskID, p.Limit)
	})

	d.Server.Handle("tasks.list", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ChannelID string `json:"channel_id"`
		}](raw)
		if err != nil {
			return nil, err
		}
		tasks, err := d.Store.TasksForChannel(p.ChannelID)
		if err != nil {
			return nil, err
		}
		out := make([]TaskView, 0, len(tasks))
		for _, t := range tasks {
			unread, err := d.Store.UnreadCount(t.ChannelID, t.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, TaskView{Task: t, Unread: unread})
		}
		return out, nil
	})

	d.Server.Handle("message.send", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ChannelID string `json:"channel_id"`
			TaskID    string `json:"task_id"`
			Body      string `json:"body"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Body) == "" {
			return nil, fmt.Errorf("empty message")
		}
		ch, err := d.Store.ChannelByID(p.ChannelID)
		if err != nil {
			return nil, err
		}
		m, err := d.PostMessage(store.Message{
			ChannelID: p.ChannelID, TaskID: p.TaskID, Author: "captain", Body: p.Body,
		})
		if err != nil {
			return nil, err
		}
		if d.Orch != nil {
			if p.TaskID == "" {
				go d.Orch.CaptainChannelMessage(context.WithoutCancel(ctx), ch, p.Body)
			} else {
				t, err := d.Store.TaskByID(p.TaskID)
				if err != nil {
					return nil, err
				}
				go d.Orch.CaptainThreadMessage(context.WithoutCancel(ctx), t, p.Body)
			}
		}
		return m, nil
	})

	d.Server.Handle("reads.mark", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ChannelID string `json:"channel_id"`
			TaskID    string `json:"task_id"`
			LastID    int64  `json:"last_id"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.Store.MarkRead(p.ChannelID, p.TaskID, p.LastID)
	})

	d.Server.Handle("instructions.get", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ChannelID string `json:"channel_id"`
		}](raw)
		if err != nil {
			return nil, err
		}
		ch, err := d.Store.ChannelByID(p.ChannelID)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(ch.InstructionsPath)
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": ch.InstructionsPath, "body": string(b)}, nil
	})
}
