// Package daemon is hq's long-lived core: sole owner of the store,
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

	"github.com/gohacki/hq/internal/config"
	"github.com/gohacki/hq/internal/rpc"
	"github.com/gohacki/hq/internal/store"
)

// Orchestrator is the judgment layer: it decides how the boss's messages
// reach agents. The daemon core stays deterministic and testable behind it.
type Orchestrator interface {
	// BossProjectMessage handles a boss message in a project's main scroll
	// (routes to the project's EM, or the director in the director room).
	BossProjectMessage(ctx context.Context, p store.Project, body string)
	// BossTicketMessage handles a boss reply inside a ticket thread (steers
	// the engineer).
	BossTicketMessage(ctx context.Context, t store.Ticket, body string)
	// Reconcile is called at boot with all non-terminal tickets.
	Reconcile(ctx context.Context, tickets []store.Ticket)
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
	if err := d.ensureDirectorRoom(); err != nil {
		return err
	}
	if d.Orch != nil {
		active, err := d.Store.ActiveTickets()
		if err != nil {
			return err
		}
		d.Orch.Reconcile(ctx, active)
	}
	d.Log.Info("daemon listening", "socket", d.Paths.SocketPath())
	return d.Server.Serve(ctx)
}

// legacyDirectorRoomName is the pre-rename name of the director's project;
// ensureDirectorRoom migrates it at boot.
const legacyDirectorRoomName = "conference-room"

// ensureDirectorRoom guarantees the built-in director project exists,
// migrating a legacy "conference-room" row (and its data dir) in place so
// history, session id, and handbook survive the rename.
func (d *Daemon) ensureDirectorRoom() error {
	_, err := d.Store.ProjectByName(store.DirectorRoomName)
	if err == nil {
		return nil
	}
	if err != store.ErrNotFound {
		return err
	}
	if p, err := d.Store.ProjectByName(legacyDirectorRoomName); err == nil {
		oldDir, newDir := d.Paths.ProjectDir(p.Name), d.Paths.ProjectDir(store.DirectorRoomName)
		if _, statErr := os.Stat(oldDir); statErr == nil {
			if err := os.Rename(oldDir, newDir); err != nil {
				return err
			}
		} else if err := os.MkdirAll(newDir, 0o755); err != nil {
			return err
		}
		return d.Store.RenameProject(p.ID, store.DirectorRoomName, filepath.Join(newDir, "handbook.md"))
	} else if err != store.ErrNotFound {
		return err
	}
	_, err = d.CreateProject(store.DirectorRoomName, nil, "local-only", "none")
	return err
}

// PostMessage appends a message and publishes it to subscribers. It is the
// single choke point every message (boss, em, eng, system) goes through.
func (d *Daemon) PostMessage(m store.Message) (store.Message, error) {
	id, err := d.Store.AppendMessage(m)
	if err != nil {
		return m, err
	}
	m.ID = id
	d.Server.Publish(rpc.EvMessageNew, m)
	return m, nil
}

// UpdateTicket persists a ticket change, publishes it, and auto-resolves any
// office items that were waiting on the ticket once it leaves waiting states.
func (d *Daemon) UpdateTicket(t store.Ticket) error {
	if err := d.Store.UpdateTicket(t); err != nil {
		return err
	}
	d.Server.Publish(rpc.EvTicketUpdated, t)
	if t.Status == store.TicketRunning || t.Status == store.TicketDelivering {
		ids, err := d.Store.ResolveTicketItems(t.ID)
		if err != nil {
			d.Log.Error("auto-resolve items", "err", err)
		}
		for _, id := range ids {
			if it, err := d.Store.ItemByID(id); err == nil {
				d.Server.Publish(rpc.EvItemResolved, it)
			}
		}
	}
	return nil
}

// FileItem creates an attention item in My Office and publishes it. The
// caller picks kind + tier per the interruption model: blocking question /
// plan review / failed = interrupt; demo / handbook proposal = break.
func (d *Daemon) FileItem(it store.Item) (store.Item, error) {
	it, err := d.Store.CreateItem(it)
	if err != nil {
		return it, err
	}
	d.Server.Publish(rpc.EvItemNew, it)
	return it, nil
}

// ResolveItem marks an office item handled and publishes the resolution.
func (d *Daemon) ResolveItem(id string) error {
	if err := d.Store.ResolveItem(id); err != nil {
		return err
	}
	if it, err := d.Store.ItemByID(id); err == nil {
		d.Server.Publish(rpc.EvItemResolved, it)
	}
	return nil
}

// --- project creation ---

var projectNameOK = func(name string) bool {
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

// CreateProject registers a project with its repos; verify controls the
// manual-verification (demo) stage for build tickets.
func (d *Daemon) CreateProject(name string, repoPaths []string, delivery, verify string) (store.Project, error) {
	if !projectNameOK(name) {
		return store.Project{}, fmt.Errorf("invalid project name %q (lowercase, digits, - _)", name)
	}
	if delivery == "" {
		delivery = "no-mistakes"
	}
	switch delivery {
	case "no-mistakes", "direct-pr", "local-only":
	default:
		return store.Project{}, fmt.Errorf("invalid delivery mode %q", delivery)
	}
	if verify == "" {
		verify = "on-completion"
	}
	switch verify {
	case "none", "before-delivery", "on-completion":
	default:
		return store.Project{}, fmt.Errorf("invalid verify mode %q (none | before-delivery | on-completion)", verify)
	}
	dir := d.Paths.ProjectDir(name)
	if err := os.MkdirAll(filepath.Join(dir, "tickets"), 0o755); err != nil {
		return store.Project{}, err
	}
	handbook := filepath.Join(dir, "handbook.md")
	if _, err := os.Stat(handbook); os.IsNotExist(err) {
		seed := fmt.Sprintf("# %s — team handbook\n\nConventions, goals, and constraints for this project. Injected into the EM's and every engineer's context.\n", name)
		if err := os.WriteFile(handbook, []byte(seed), 0o644); err != nil {
			return store.Project{}, err
		}
	}
	p := store.Project{
		ID:           store.NewID("prj"),
		Name:         name,
		Delivery:     delivery,
		Verify:       verify,
		HandbookPath: handbook,
	}
	if err := d.Store.CreateProject(p); err != nil {
		return store.Project{}, err
	}
	for _, rp := range repoPaths {
		if _, err := d.addRepo(p, rp); err != nil {
			return store.Project{}, fmt.Errorf("repo %s: %w", rp, err)
		}
	}
	d.Server.Publish(rpc.EvProjectCreated, p)
	return p, nil
}

// addRepo registers a local repo with a project. Nothing is written into
// the repo itself — worktrees are native git worktrees under hq's data dir.
func (d *Daemon) addRepo(p store.Project, path string) (store.Repo, error) {
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
		ProjectID:     p.ID,
		Name:          filepath.Base(abs),
		Path:          abs,
		DefaultBranch: branch,
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


// --- RPC handlers ---

func unmarshal[T any](raw json.RawMessage) (T, error) {
	var v T
	if raw == nil {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

// ProjectView is a project plus derived UI state.
type ProjectView struct {
	store.Project
	Unread int          `json:"unread"`
	Repos  []store.Repo `json:"repos"`
}

// TicketView is a ticket plus derived UI state.
type TicketView struct {
	store.Ticket
	Unread  int    `json:"unread"`
	Project string `json:"project"` // project name (board/office rendering)
}

func (d *Daemon) registerHandlers() {
	d.Server.Handle("ping", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return "pong", nil
	})

	d.Server.Handle("projects.list", func(ctx context.Context, _ json.RawMessage) (any, error) {
		prjs, err := d.Store.Projects()
		if err != nil {
			return nil, err
		}
		out := make([]ProjectView, 0, len(prjs))
		for _, p := range prjs {
			unread, err := d.Store.UnreadCount(p.ID, "")
			if err != nil {
				return nil, err
			}
			repos, err := d.Store.ReposForProject(p.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, ProjectView{Project: p, Unread: unread, Repos: repos})
		}
		return out, nil
	})

	d.Server.Handle("projects.create", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			Name     string   `json:"name"`
			Repos    []string `json:"repos"`
			Delivery string   `json:"delivery"`
			Verify   string   `json:"verify"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return d.CreateProject(p.Name, p.Repos, p.Delivery, p.Verify)
	})

	d.Server.Handle("messages.list", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ProjectID string `json:"project_id"`
			TicketID  string `json:"ticket_id"`
			Limit     int    `json:"limit"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return d.Store.Messages(p.ProjectID, p.TicketID, p.Limit)
	})

	d.Server.Handle("tickets.list", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ProjectID string `json:"project_id"` // "" = all projects (board / director)
		}](raw)
		if err != nil {
			return nil, err
		}
		var tickets []store.Ticket
		if p.ProjectID == "" {
			tickets, err = d.Store.AllTickets()
		} else {
			tickets, err = d.Store.TicketsForProject(p.ProjectID)
		}
		if err != nil {
			return nil, err
		}
		names := map[string]string{}
		if prjs, err := d.Store.Projects(); err == nil {
			for _, pr := range prjs {
				names[pr.ID] = pr.Name
			}
		}
		out := make([]TicketView, 0, len(tickets))
		for _, t := range tickets {
			unread, err := d.Store.UnreadCount(t.ProjectID, t.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, TicketView{Ticket: t, Unread: unread, Project: names[t.ProjectID]})
		}
		return out, nil
	})

	d.Server.Handle("message.send", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ProjectID string `json:"project_id"`
			TicketID  string `json:"ticket_id"`
			Body      string `json:"body"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Body) == "" {
			return nil, fmt.Errorf("empty message")
		}
		prj, err := d.Store.ProjectByID(p.ProjectID)
		if err != nil {
			return nil, err
		}
		m, err := d.PostMessage(store.Message{
			ProjectID: p.ProjectID, TicketID: p.TicketID, Author: "boss", Body: p.Body,
		})
		if err != nil {
			return nil, err
		}
		if d.Orch != nil {
			if p.TicketID == "" {
				go d.Orch.BossProjectMessage(context.WithoutCancel(ctx), prj, p.Body)
			} else {
				t, err := d.Store.TicketByID(p.TicketID)
				if err != nil {
					return nil, err
				}
				go d.Orch.BossTicketMessage(context.WithoutCancel(ctx), t, p.Body)
			}
		}
		return m, nil
	})

	d.Server.Handle("reads.mark", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ProjectID string `json:"project_id"`
			TicketID  string `json:"ticket_id"`
			LastID    int64  `json:"last_id"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.Store.MarkRead(p.ProjectID, p.TicketID, p.LastID)
	})

	d.Server.Handle("items.list", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return d.Store.OpenItems()
	})

	d.Server.Handle("item.resolve", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ID string `json:"id"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return "resolved", d.ResolveItem(p.ID)
	})

	d.Server.Handle("presence.get", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return d.Store.GetSetting("presence", "available")
	})

	d.Server.Handle("presence.set", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			Mode string `json:"mode"`
		}](raw)
		if err != nil {
			return nil, err
		}
		switch p.Mode {
		case "heads-down", "available", "review":
		default:
			return nil, fmt.Errorf("mode must be heads-down | available | review")
		}
		return p.Mode, d.Store.SetSetting("presence", p.Mode)
	})

	d.Server.Handle("handbook.get", func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := unmarshal[struct {
			ProjectID string `json:"project_id"`
		}](raw)
		if err != nil {
			return nil, err
		}
		prj, err := d.Store.ProjectByID(p.ProjectID)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(prj.HandbookPath)
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": prj.HandbookPath, "body": string(b)}, nil
	})
}
