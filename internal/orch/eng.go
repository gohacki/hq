package orch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gohacki/hq/internal/agent"
	"github.com/gohacki/hq/internal/store"
	"github.com/gohacki/hq/internal/worktree"
)

type engRun struct {
	session agent.Session
}

// engModel resolves the model an engineer runs on: per-ticket override, else
// the project's engineer default, else the harness default.
func (o *Orch) engModel(t store.Ticket) string {
	if t.Model != "" {
		return t.Model
	}
	if p, err := o.d.Store.ProjectByID(t.ProjectID); err == nil {
		return p.EngModel
	}
	return ""
}

// createTicket is the whole deterministic engineer hire: record → worktree
// lease → brief on disk → headless agent in the worktree.
func (o *Orch) createTicket(ctx context.Context, projectID, repoName, kind, title, brief, model string) (store.Ticket, error) {
	if kind != "build" && kind != "spike" {
		return store.Ticket{}, fmt.Errorf("kind must be build or spike")
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(brief) == "" {
		return store.Ticket{}, fmt.Errorf("title and brief are required")
	}
	p, err := o.d.Store.ProjectByID(projectID)
	if err != nil {
		return store.Ticket{}, err
	}
	repos, err := o.d.Store.ReposForProject(projectID)
	if err != nil {
		return store.Ticket{}, err
	}
	if len(repos) == 0 {
		return store.Ticket{}, fmt.Errorf("project %s has no repos registered", p.Name)
	}
	var repo store.Repo
	if repoName == "" && len(repos) == 1 {
		repo = repos[0]
	} else {
		for _, r := range repos {
			if r.Name == repoName {
				repo = r
				break
			}
		}
		if repo.ID == "" {
			names := make([]string, len(repos))
			for i, r := range repos {
				names[i] = r.Name
			}
			return store.Ticket{}, fmt.Errorf("unknown repo %q (project has: %s)", repoName, strings.Join(names, ", "))
		}
	}

	t := store.Ticket{
		ID:        store.NewID("tkt"),
		ProjectID: p.ID,
		RepoID:    repo.ID,
		Kind:      kind,
		Title:     title,
		Status:    store.TicketQueued,
		Model:     model,
	}
	ticketDir := o.d.Paths.TicketDir(p.Name, t.ID)
	if err := os.MkdirAll(ticketDir, 0o755); err != nil {
		return store.Ticket{}, err
	}
	t.BriefPath = filepath.Join(ticketDir, "brief.md")
	if kind == "spike" {
		t.ReportPath = filepath.Join(ticketDir, "report.md")
	}
	if err := o.d.Store.CreateTicket(t); err != nil {
		return store.Ticket{}, err
	}

	fullBrief := engBrief(p, t, repo, brief)
	if err := os.WriteFile(t.BriefPath, []byte(fullBrief), 0o644); err != nil {
		return store.Ticket{}, err
	}

	if _, err := o.d.PostMessage(store.Message{
		ProjectID: p.ID, TicketID: t.ID, Author: "system", Kind: "system",
		Body: fmt.Sprintf("ticket opened: %s (%s, repo %s)", title, kind, repo.Name),
	}); err != nil {
		o.log.Error("post ticket-opened message", "err", err)
	}

	go o.spawnEng(ctx, p, t, repo, fullBrief)
	return t, nil
}

// retryTicket re-opens a failed ticket as a fresh one with the same brief.
func (o *Orch) retryTicket(ctx context.Context, ticketID string) (store.Ticket, error) {
	old, err := o.d.Store.TicketByID(ticketID)
	if err != nil {
		return store.Ticket{}, err
	}
	if old.Status != store.TicketFailed && old.Status != store.TicketAbandoned {
		return store.Ticket{}, fmt.Errorf("ticket %s is %s — retry applies to failed/abandoned tickets", old.ID, old.Status)
	}
	brief, err := os.ReadFile(old.BriefPath)
	if err != nil {
		return store.Ticket{}, fmt.Errorf("original brief unreadable: %w", err)
	}
	repos, err := o.d.Store.ReposForProject(old.ProjectID)
	if err != nil {
		return store.Ticket{}, err
	}
	repoName := ""
	for _, r := range repos {
		if r.ID == old.RepoID {
			repoName = r.Name
		}
	}
	if _, err := o.d.PostMessage(store.Message{
		ProjectID: old.ProjectID, TicketID: old.ID, Author: "system", Kind: "system",
		Body: "retried as a new ticket",
	}); err != nil {
		o.log.Error("post retry message", "err", err)
	}
	// The stored brief is the fully rendered version; the wrapper repeats
	// but the context is identical.
	return o.createTicket(ctx, old.ProjectID, repoName, old.Kind, old.Title, string(brief), old.Model)
}

func (o *Orch) spawnEng(ctx context.Context, p store.Project, t store.Ticket, repo store.Repo, prompt string) {
	wt, err := worktree.Lease(repo.Path, "hq:"+t.ID)
	if err != nil {
		o.failTicket(t, "worktree lease failed: "+err.Error())
		return
	}
	t.WorktreePath = wt
	t.Status = store.TicketRunning
	if err := o.d.UpdateTicket(t); err != nil {
		o.log.Error("update ticket", "err", err)
	}

	sess, err := o.harness.Start(ctx, agent.Spec{
		Model:      o.engModel(t),
		WorkDir:    wt,
		Prompt:     prompt,
		Autonomous: true, // isolated worktree
	})
	if err != nil {
		o.failTicket(t, "agent start failed: "+err.Error())
		return
	}
	o.mu.Lock()
	o.engs[t.ID] = &engRun{session: sess}
	o.mu.Unlock()
	o.superviseEng(t, sess)
}

// sendToEng delivers a message to a live engineer, resuming its session in
// the worktree if the process is gone (restart, idle reap, desk visit).
func (o *Orch) sendToEng(ctx context.Context, t store.Ticket, body string) error {
	if t.Status.Terminal() {
		return fmt.Errorf("ticket %s is %s", t.ID, t.Status)
	}
	o.mu.Lock()
	c, ok := o.engs[t.ID]
	o.mu.Unlock()
	if ok {
		if err := c.session.Send(body); err == nil {
			return nil
		}
		o.dropEng(t.ID)
	}
	if t.SessionID == "" {
		return fmt.Errorf("engineer has no session to resume")
	}
	if t.WorktreePath == "" {
		return fmt.Errorf("engineer has no worktree")
	}
	sess, err := o.harness.Start(ctx, agent.Spec{
		Model:           o.engModel(t),
		WorkDir:         t.WorktreePath,
		Prompt:          body,
		ResumeSessionID: t.SessionID,
		Autonomous:      true,
	})
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.engs[t.ID] = &engRun{session: sess}
	o.mu.Unlock()
	t.Status = store.TicketRunning
	if err := o.d.UpdateTicket(t); err != nil {
		o.log.Error("update ticket", "err", err)
	}
	go o.superviseEng(t, sess)
	return nil
}

func (o *Orch) dropEng(ticketID string) {
	o.mu.Lock()
	if c, ok := o.engs[ticketID]; ok {
		c.session.Close()
		delete(o.engs, ticketID)
	}
	o.mu.Unlock()
}

var (
	reStatusDone    = regexp.MustCompile(`(?m)^STATUS:\s*done\b(.*)$`)
	reStatusBlocked = regexp.MustCompile(`(?m)^STATUS:\s*blocked\b(.*)$`)
	reQuestion      = regexp.MustCompile(`(?m)^QUESTION:\s*(.+)$`)
	reDemo          = regexp.MustCompile(`(?m)^DEMO:\s*(.+)$`)
)

// superviseEng is the deterministic supervisor for one engineer process:
// stream text into the ticket thread, classify turn-ends by protocol
// markers, and file office items only for actionable states.
func (o *Orch) superviseEng(t store.Ticket, sess agent.Session) {
	post := func(kind, body string) {
		if _, err := o.d.PostMessage(store.Message{
			ProjectID: t.ProjectID, TicketID: t.ID, Author: "eng:" + t.ID, Kind: kind, Body: body,
		}); err != nil {
			o.log.Error("post eng message", "err", err)
		}
	}
	refresh := func() {
		if cur, err := o.d.Store.TicketByID(t.ID); err == nil {
			t = cur
		}
	}

	for ev := range sess.Events() {
		switch ev.Kind {
		case agent.EvInit:
			refresh()
			if ev.SessionID != "" && ev.SessionID != t.SessionID {
				t.SessionID = ev.SessionID
				if err := o.d.UpdateTicket(t); err != nil {
					o.log.Error("persist eng session", "err", err)
				}
			}
		case agent.EvText:
			post("text", ev.Text)
		case agent.EvResult:
			refresh()
			if t.Status.Terminal() || t.Status == store.TicketVisiting {
				continue
			}
			o.classifyTurnEnd(&t, ev, post)
		case agent.EvExited:
			o.mu.Lock()
			delete(o.engs, t.ID)
			o.mu.Unlock()
			refresh()
			if !t.Status.Terminal() && t.Status != store.TicketVisiting && t.Status != store.TicketNeedsInput && t.Status != store.TicketBlocked {
				t.Status = store.TicketFailed
				if err := o.d.UpdateTicket(t); err != nil {
					o.log.Error("update ticket", "err", err)
				}
				post("system", "engineer process exited unexpectedly")
				o.fileTicketItem(t, store.ItemFailed, store.TierInterrupt, "engineer crashed", "reply in the thread, or retry the ticket (r)")
			}
			return
		}
	}
}

// classifyTurnEnd maps a finished engineer turn onto a ticket status using
// the brief's mandatory protocol markers. Deterministic — no model in the
// loop. DEMO: files a break-tier item (manual verification); QUESTION: and
// blocked/failed file interrupts.
func (o *Orch) classifyTurnEnd(t *store.Ticket, ev agent.Event, post func(kind, body string)) {
	text := ev.Text
	isDemo := reDemo.MatchString(text)
	switch {
	case ev.IsError:
		t.Status = store.TicketFailed
		post("system", "engineer turn errored: "+text)
	case isDemo, reQuestion.MatchString(text):
		t.Status = store.TicketNeedsInput
	case reStatusBlocked.MatchString(text):
		t.Status = store.TicketBlocked
	case reStatusDone.MatchString(text):
		t.Status = store.TicketDone
		if t.Kind == "spike" {
			o.postSpikeReport(t, post)
		}
	default:
		// Protocol violation: turn ended without a marker. Surface it rather
		// than guessing.
		t.Status = store.TicketNeedsInput
		post("system", "engineer ended its turn without a DEMO/STATUS/QUESTION line — reply to steer it")
	}
	if err := o.d.UpdateTicket(*t); err != nil {
		o.log.Error("update ticket", "err", err)
	}
	switch {
	case t.Status == store.TicketNeedsInput && isDemo:
		o.fileTicketItem(*t, store.ItemDemo, store.TierBreak, "demo ready", text)
	case t.Status == store.TicketNeedsInput && reQuestion.MatchString(text):
		o.fileTicketItem(*t, store.ItemQuestion, store.TierInterrupt, "question", reQuestion.FindStringSubmatch(text)[1])
	case t.Status == store.TicketNeedsInput:
		o.fileTicketItem(*t, store.ItemQuestion, store.TierInterrupt, "needs steering", "turn ended without a protocol marker")
	case t.Status == store.TicketBlocked:
		o.fileTicketItem(*t, store.ItemBlocked, store.TierInterrupt, "blocked", text)
	case t.Status == store.TicketFailed:
		o.fileTicketItem(*t, store.ItemFailed, store.TierInterrupt, "failed", text)
	case t.Status == store.TicketDone:
		o.dropEng(t.ID)
		go o.teardown(*t)
	}
}

// postSpikeReport renders the spike's report file into the thread as a rich
// report message.
func (o *Orch) postSpikeReport(t *store.Ticket, post func(kind, body string)) {
	b, err := os.ReadFile(t.ReportPath)
	if err != nil {
		post("system", "spike finished but report file is missing: "+err.Error())
		return
	}
	post("report", string(b))
}

func (o *Orch) failTicket(t store.Ticket, msg string) {
	t.Status = store.TicketFailed
	if err := o.d.UpdateTicket(t); err != nil {
		o.log.Error("update ticket", "err", err)
	}
	o.systemMessage(t.ProjectID, t.ID, msg)
	o.fileTicketItem(t, store.ItemFailed, store.TierInterrupt, "failed", msg)
}

// ProposedTickets extracts the "## Proposed tickets" bullets from a spike
// report ("## Proposed tasks" accepted for tolerance).
func ProposedTickets(report string) []string {
	var out []string
	inSection := false
	for _, line := range strings.Split(report, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			h := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			inSection = h == "proposed tickets" || h == "proposed tasks"
			continue
		}
		if inSection && strings.HasPrefix(trimmed, "- ") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		}
	}
	return out
}

// handoff promotes one "Proposed tickets" bullet from a spike report into a
// new build ticket, attaching the full report as context.
func (o *Orch) handoff(ctx context.Context, spikeTicketID, proposal string) (store.Ticket, error) {
	spike, err := o.d.Store.TicketByID(spikeTicketID)
	if err != nil {
		return store.Ticket{}, err
	}
	if spike.Kind != "spike" || spike.ReportPath == "" {
		return store.Ticket{}, fmt.Errorf("ticket %s is not a spike with a report", spikeTicketID)
	}
	report, err := os.ReadFile(spike.ReportPath)
	if err != nil {
		return store.Ticket{}, err
	}
	repos, err := o.d.Store.ReposForProject(spike.ProjectID)
	if err != nil {
		return store.Ticket{}, err
	}
	repoName := ""
	for _, r := range repos {
		if r.ID == spike.RepoID {
			repoName = r.Name
		}
	}
	brief := fmt.Sprintf(`Implement this proposal from spike %s ("%s"):

%s

The spike's full report follows — it is your primary context.

---

%s`, spike.ID, spike.Title, proposal, string(report))
	return o.createTicket(ctx, spike.ProjectID, repoName, "build", proposal, brief, "")
}
