// Package orch is hq's orchestration layer: it runs the director, EMs, and
// engineers on top of the daemon core. The daemon stays deterministic; orch
// owns everything that touches an agent process.
package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gohacki/hq/internal/agent"
	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/playbook"
	"github.com/gohacki/hq/internal/store"
)

// emIdleTimeout closes an idle EM/director process; its session resumes on the
// next message, so this only trades warm-start latency for memory.
const emIdleTimeout = 15 * time.Minute

type Orch struct {
	d       *daemon.Daemon
	harness agent.Harness
	log     *slog.Logger

	mu        sync.Mutex
	ems       map[string]*em                // project id → live EM/director process
	engs      map[string]*engRun            // ticket id → live engineer process
	visitPrev map[string]store.TicketStatus // ticket id → status to restore on checkin
}

type em struct {
	session agent.Session
	lastUse time.Time
}

func New(d *daemon.Daemon, h agent.Harness, log *slog.Logger) *Orch {
	o := &Orch{d: d, harness: h, log: log, ems: map[string]*em{}, engs: map[string]*engRun{}, visitPrev: map[string]store.TicketStatus{}}
	o.registerHandlers()
	go o.reapIdleEMs()
	return o
}

// --- daemon.Orchestrator ---

func (o *Orch) BossProjectMessage(ctx context.Context, p store.Project, body string) {
	if err := o.sendToEM(ctx, p, body); err != nil {
		o.log.Error("em send failed", "project", p.Name, "err", err)
		o.systemMessage(p.ID, "", "manager agent failed: "+err.Error())
	}
}

func (o *Orch) BossTicketMessage(ctx context.Context, t store.Ticket, body string) {
	if err := o.sendToEng(ctx, t, body); err != nil {
		o.log.Error("eng send failed", "ticket", t.ID, "err", err)
		o.systemMessage(t.ProjectID, t.ID, "could not reach engineer: "+err.Error())
	}
}

// Reconcile runs at daemon boot: a headless engineer process actually
// running or mid-delivery did not survive the restart, so park those.
// Tickets already sitting in needs-input/blocked (a demo, question, or
// failure already correctly waiting on the boss — nothing was lost) are
// left alone; parking them anyway would post a misleading "parked by
// restart" message and a duplicate item next to whatever legitimately got
// them there.
//
// Visiting tickets get the same treatment even though a live tmux chat
// pane *might* still be running independent of the daemon: the daemon's
// own in-memory ownership of that session (checkoutEngineer's bookkeeping)
// is gone regardless of the pane's fate, and it has no way to tell "pane
// still open" from "whole hq session died with it, ticket orphaned in
// visiting forever" — needs-input is the safe default either way. If the
// pane really is still open, its eventual close just finds the status
// already moved on and no-ops (checkinEngineer only acts on TicketVisiting).
func (o *Orch) Reconcile(ctx context.Context, tickets []store.Ticket) {
	go o.sweepTeardowns()
	for _, t := range tickets {
		if t.Status != store.TicketRunning && t.Status != store.TicketDelivering && t.Status != store.TicketVisiting {
			continue
		}
		t.Status = store.TicketNeedsInput
		if err := o.d.UpdateTicket(t); err != nil {
			o.log.Error("reconcile update failed", "ticket", t.ID, "err", err)
			continue
		}
		o.systemMessage(t.ProjectID, t.ID,
			"hq restarted — engineer session parked. Reply in this thread to resume it.")
		o.fileTicketItem(t, store.ItemBlocked, store.TierInterrupt, "parked by restart — reply to resume", "")
	}
}

func (o *Orch) systemMessage(projectID, ticketID, body string) {
	if _, err := o.d.PostMessage(store.Message{
		ProjectID: projectID, TicketID: ticketID, Author: "system", Kind: "system", Body: body,
	}); err != nil {
		o.log.Error("system message failed", "err", err)
	}
}

// fileTicketItem creates an office item for a ticket, deduping is left to
// item auto-resolution (ticket leaving its waiting state).
func (o *Orch) fileTicketItem(t store.Ticket, kind store.ItemKind, tier store.ItemTier, title, body string) {
	if _, err := o.d.FileItem(store.Item{
		Kind: kind, Tier: tier, ProjectID: t.ProjectID, TicketID: t.ID,
		Title: fmt.Sprintf("%s — %s", title, t.Title), Body: body,
	}); err != nil {
		o.log.Error("file item", "err", err)
	}
}

// --- EM / director lifecycle (same machinery; the director is the home
// room's EM with a different prompt and tool set) ---

func (o *Orch) isDirectorRoom(p store.Project) bool {
	return p.Name == store.DirectorRoomName
}

func (o *Orch) sendToEM(ctx context.Context, p store.Project, body string) error {
	o.mu.Lock()
	l, ok := o.ems[p.ID]
	if ok {
		l.lastUse = time.Now()
	}
	o.mu.Unlock()
	if ok {
		if err := l.session.Send(body); err == nil {
			return nil
		}
		o.dropEM(p.ID)
	}
	return o.startEM(ctx, p, body)
}

func (o *Orch) dropEM(projectID string) {
	o.mu.Lock()
	if l, ok := o.ems[projectID]; ok {
		l.session.Close()
		delete(o.ems, projectID)
	}
	o.mu.Unlock()
}

func (o *Orch) startEM(ctx context.Context, p store.Project, prompt string) error {
	mcpPath, err := o.writeEMMCPConfig(p)
	if err != nil {
		return err
	}
	sysPrompt := emSystemPrompt(p)
	if o.isDirectorRoom(p) {
		sysPrompt = directorSystemPrompt()
	}
	spec := agent.Spec{
		Model:           p.EMModel,
		WorkDir:         o.d.Paths.ProjectDir(p.Name),
		SystemPrompt:    sysPrompt,
		Prompt:          prompt,
		ResumeSessionID: p.EMSessionID,
		MCPConfigPath:   mcpPath,
		Autonomous:      true, // full tool access; delegate-don't-do is prompt-enforced
	}
	sess, err := o.harness.Start(ctx, spec)
	if err != nil && p.EMSessionID != "" {
		// Stale session id (e.g. claude storage cleaned) — start fresh.
		spec.ResumeSessionID = ""
		sess, err = o.harness.Start(ctx, spec)
	}
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.ems[p.ID] = &em{session: sess, lastUse: time.Now()}
	o.mu.Unlock()
	go o.pumpEM(p, sess)
	return nil
}

func (o *Orch) pumpEM(p store.Project, sess agent.Session) {
	// The director (home chat) and project EMs share the author tag —
	// routing keys on "em"; the TUI relabels it "director" in the home chat.
	author := "em"
	for ev := range sess.Events() {
		switch ev.Kind {
		case agent.EvInit:
			if ev.SessionID != "" && ev.SessionID != p.EMSessionID {
				if err := o.d.Store.SetProjectEMSession(p.ID, ev.SessionID); err != nil {
					o.log.Error("persist em session", "err", err)
				}
				p.EMSessionID = ev.SessionID
			}
		case agent.EvText:
			if _, err := o.d.PostMessage(store.Message{
				ProjectID: p.ID, Author: author, Body: ev.Text,
			}); err != nil {
				o.log.Error("post em message", "err", err)
			}
		case agent.EvResult:
			if ev.IsError {
				o.systemMessage(p.ID, "", author+" turn errored: "+ev.Text)
			}
		case agent.EvExited:
			o.mu.Lock()
			delete(o.ems, p.ID)
			o.mu.Unlock()
			return
		}
	}
}

func (o *Orch) reapIdleEMs() {
	for range time.Tick(time.Minute) {
		o.mu.Lock()
		for id, l := range o.ems {
			if time.Since(l.lastUse) > emIdleTimeout {
				l.session.Close()
				delete(o.ems, id)
			}
		}
		o.mu.Unlock()
	}
}

// writeEMMCPConfig writes the MCP config file pointing Claude at this
// project's hq tool server (a subcommand of our own binary).
func (o *Orch) writeEMMCPConfig(p store.Project) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	args := []string{"mcp-em", "--project", p.ID}
	if o.isDirectorRoom(p) {
		args = append(args, "--director")
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"hq": map[string]any{"command": exe, "args": args},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	dir := o.d.Paths.ProjectDir(p.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func modelOK(m string) bool {
	switch m {
	case "sonnet", "opus", "fable", "haiku":
		return true
	}
	// Full model ids pass through (e.g. claude-sonnet-5).
	return strings.HasPrefix(m, "claude-")
}

// --- RPC handlers (agent-facing + office actions) ---

func (o *Orch) registerHandlers() {
	srv := o.d.Server

	// Overrides the daemon's bare handler: after creating the project, seed
	// each repo's onboarding doc (cached scout) so the handbook learns how
	// local development works.
	srv.Handle("projects.create", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Name     string   `json:"name"`
			Repos    []string `json:"repos"`
			Delivery string   `json:"delivery"`
			Verify   string   `json:"verify"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		prj, err := o.d.CreateProject(p.Name, p.Repos, p.Delivery, p.Verify)
		if err != nil {
			return nil, err
		}
		repos, err := o.d.Store.ReposForProject(prj.ID)
		if err != nil {
			return nil, err
		}
		bg := context.WithoutCancel(ctx)
		for _, r := range repos {
			o.seedOnboarding(bg, prj, r)
		}
		o.systemMessage(prj.ID, "", "setup interview pending — say hello and the EM will walk you through this project's lifecycle (worktrees, dev servers, pipeline, delivery) into its playbook")
		return prj, nil
	})

	srv.Handle("ticket.create", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			Repo      string `json:"repo"`
			Kind      string `json:"kind"`
			Title     string `json:"title"`
			Brief     string `json:"brief"`
			Model     string `json:"model"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Model != "" && !modelOK(p.Model) {
			return nil, fmt.Errorf("unknown model %q (sonnet | opus | fable | haiku)", p.Model)
		}
		return o.createTicket(context.WithoutCancel(ctx), p.ProjectID, p.Repo, p.Kind, p.Title, p.Brief, p.Model)
	})

	srv.Handle("tickets.create_batch", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			Tickets   []struct {
				Repo  string `json:"repo"`
				Kind  string `json:"kind"`
				Title string `json:"title"`
				Brief string `json:"brief"`
				Model string `json:"model"`
			} `json:"tickets"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if len(p.Tickets) == 0 {
			return nil, fmt.Errorf("tickets is empty")
		}
		bg := context.WithoutCancel(ctx)
		type result struct {
			Title string `json:"title"`
			ID    string `json:"id,omitempty"`
			Error string `json:"error,omitempty"`
		}
		out := make([]result, 0, len(p.Tickets))
		for _, spec := range p.Tickets {
			t, err := o.createTicket(bg, p.ProjectID, spec.Repo, spec.Kind, spec.Title, spec.Brief, spec.Model)
			r := result{Title: spec.Title}
			if err != nil {
				r.Error = err.Error()
			} else {
				r.ID = t.ID
			}
			out = append(out, r)
		}
		return out, nil
	})

	srv.Handle("ticket.message", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TicketID string `json:"ticket_id"`
			Body     string `json:"body"`
			Author   string `json:"author"` // em (from MCP) — boss goes via message.send
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		t, err := o.d.Store.TicketByID(p.TicketID)
		if err != nil {
			return nil, err
		}
		if p.Author == "" {
			p.Author = "em"
		}
		if _, err := o.d.PostMessage(store.Message{
			ProjectID: t.ProjectID, TicketID: t.ID, Author: p.Author, Body: p.Body,
		}); err != nil {
			return nil, err
		}
		if err := o.sendToEng(context.WithoutCancel(ctx), t, p.Body); err != nil {
			return nil, fmt.Errorf("posted, but engineer unreachable: %w", err)
		}
		return "sent", nil
	})

	srv.Handle("ticket.retry", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TicketID string `json:"ticket_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.retryTicket(context.WithoutCancel(ctx), p.TicketID)
	})

	srv.Handle("ticket.abandon", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TicketID string `json:"ticket_id"`
			Force    bool   `json:"force"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.abandonTicket(p.TicketID, p.Force)
	})

	srv.Handle("report.read", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TicketID string `json:"ticket_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		t, err := o.d.Store.TicketByID(p.TicketID)
		if err != nil {
			return nil, err
		}
		if t.ReportPath == "" {
			return nil, fmt.Errorf("ticket %s has no report", t.ID)
		}
		b, err := os.ReadFile(t.ReportPath)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	})

	srv.Handle("handbook.set", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			Body      string `json:"body"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		prj, err := o.d.Store.ProjectByID(p.ProjectID)
		if err != nil {
			return nil, err
		}
		return "saved", os.WriteFile(prj.HandbookPath, []byte(p.Body), 0o644)
	})

	srv.Handle("onboarding.refresh", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			Repo      string `json:"repo"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.refreshOnboarding(context.WithoutCancel(ctx), p.ProjectID, p.Repo)
	})

	// session.checkout/checkin hand a live session over to the human's
	// terminal for interactive use and back. The daemon never owns a
	// terminal — it just returns the argv+dir the TUI needs to spawn the
	// real interactive harness CLI itself, embedded in its own frame.
	srv.Handle("session.checkout", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			TicketID  string `json:"ticket_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.TicketID != "" {
			return o.checkoutEngineer(p.TicketID)
		}
		return o.checkoutEM(p.ProjectID)
	})

	srv.Handle("session.checkin", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			TicketID  string `json:"ticket_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.TicketID != "" {
			return "checked in", o.checkinEngineer(p.TicketID)
		}
		return "checked in", o.checkinEM(p.ProjectID)
	})

	srv.Handle("model.set", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			TicketID  string `json:"ticket_id"`
			Scope     string `json:"scope"` // ticket | em | eng
			Model     string `json:"model"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if !modelOK(p.Model) {
			return nil, fmt.Errorf("unknown model %q (sonnet | opus | fable | haiku)", p.Model)
		}
		return o.setModel(context.WithoutCancel(ctx), p.ProjectID, p.TicketID, p.Scope, p.Model)
	})

	srv.Handle("project.settings.set", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
			Delivery  string `json:"delivery"` // "" = leave unchanged
			Verify    string `json:"verify"`   // "" = leave unchanged
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Delivery != "" {
			switch p.Delivery {
			case "no-mistakes", "direct-pr", "local-only":
			default:
				return nil, fmt.Errorf("unknown delivery mode %q (no-mistakes | direct-pr | local-only)", p.Delivery)
			}
			if err := o.d.Store.SetProjectDelivery(p.ProjectID, p.Delivery); err != nil {
				return nil, err
			}
		}
		if p.Verify != "" {
			switch p.Verify {
			case "none", "before-delivery", "on-completion":
			default:
				return nil, fmt.Errorf("unknown verify mode %q (none | before-delivery | on-completion)", p.Verify)
			}
			if err := o.d.Store.SetProjectVerify(p.ProjectID, p.Verify); err != nil {
				return nil, err
			}
		}
		prj, err := o.d.Store.ProjectByID(p.ProjectID)
		if err != nil {
			return nil, err
		}
		o.systemMessage(p.ProjectID, "", fmt.Sprintf("project settings updated — delivery: %s, verify: %s", prj.Delivery, prj.Verify))
		return fmt.Sprintf("%s → delivery: %s, verify: %s", prj.Name, prj.Delivery, prj.Verify), nil
	})

	srv.Handle("playbook.get", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		prj, err := o.d.Store.ProjectByID(p.ProjectID)
		if err != nil {
			return nil, err
		}
		pb, err := playbook.Load(o.d.Paths.ProjectDir(prj.Name))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"exists": playbook.Exists(o.d.Paths.ProjectDir(prj.Name)),
			"prose":  pb.Prose,
			"repos":  pb.Repos,
		}, nil
	})

	srv.Handle("playbook.set", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ProjectID string                          `json:"project_id"`
			Prose     string                          `json:"prose"`
			Repos     map[string]playbook.RepoRecipe `json:"repos"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		prj, err := o.d.Store.ProjectByID(p.ProjectID)
		if err != nil {
			return nil, err
		}
		// Merge semantics: omitted halves keep their current value, so the
		// EM can refine prose and recipes independently.
		cur, err := playbook.Load(o.d.Paths.ProjectDir(prj.Name))
		if err != nil {
			return nil, err
		}
		if p.Prose != "" {
			cur.Prose = p.Prose
		}
		for name, r := range p.Repos {
			cur.Repos[name] = r
		}
		if err := playbook.Save(o.d.Paths.ProjectDir(prj.Name), cur); err != nil {
			return nil, err
		}
		o.systemMessage(prj.ID, "", "playbook updated — press p on the project to read it")
		return "saved", nil
	})

	o.registerPlanHandlers()
	o.registerDeleteHandler()
}

// setModel changes an agent's model on the fly. Live sessions are dropped
// and resumed with the new model — same session id, no context lost.
func (o *Orch) setModel(ctx context.Context, projectID, ticketID, scope, model string) (string, error) {
	switch scope {
	case "ticket":
		t, err := o.d.Store.TicketByID(ticketID)
		if err != nil {
			return "", err
		}
		if t.Status.Terminal() {
			return "", fmt.Errorf("ticket %s is %s", t.ID, t.Status)
		}
		o.mu.Lock()
		_, wasLive := o.engs[t.ID]
		o.mu.Unlock()
		t.Model = model
		// Park first so the supervisor's exit handler doesn't read the
		// intentional kill as a crash.
		if wasLive {
			t.Status = store.TicketNeedsInput
		}
		if err := o.d.UpdateTicket(t); err != nil {
			return "", err
		}
		o.dropEng(t.ID)
		o.systemMessage(t.ProjectID, t.ID, "engineer model → "+model)
		if wasLive && t.SessionID != "" {
			if err := o.sendToEng(ctx, t,
				"(your model was switched to "+model+" — continue exactly where you left off)"); err != nil {
				return "", fmt.Errorf("model saved, but resume failed: %w", err)
			}
		}
		return "ticket " + t.ID + " → " + model, nil
	case "em":
		p, err := o.d.Store.ProjectByID(projectID)
		if err != nil {
			return "", err
		}
		if err := o.d.Store.SetProjectEMModel(p.ID, model); err != nil {
			return "", err
		}
		o.dropEM(p.ID) // next message resumes the session on the new model
		o.systemMessage(p.ID, "", "manager model → "+model)
		return p.Name + " manager → " + model, nil
	case "eng":
		p, err := o.d.Store.ProjectByID(projectID)
		if err != nil {
			return "", err
		}
		if err := o.d.Store.SetProjectEngModel(p.ID, model); err != nil {
			return "", err
		}
		o.systemMessage(p.ID, "", "default engineer model → "+model+" (future tickets)")
		return p.Name + " engineer default → " + model, nil
	default:
		return "", fmt.Errorf("scope must be ticket, em, or eng")
	}
}
