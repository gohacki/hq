package orch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gohacki/hq/internal/agent"
	"github.com/gohacki/hq/internal/playbook"
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

	// The whole worktree set's paths are deterministic, so the brief can
	// name every sibling before anything is created (spawnEng creates them).
	pb, err := playbook.Load(o.d.Paths.ProjectDir(p.Name))
	if err != nil {
		o.log.Error("playbook load", "project", p.Name, "err", err)
	}
	trees := plannedTrees(o.d.Paths.WorktreesDir(), t.ID, repo, repos)
	fullBrief := engBrief(p, t, repo, brief, pb, trees)
	if err := os.WriteFile(t.BriefPath, []byte(fullBrief), 0o644); err != nil {
		return store.Ticket{}, err
	}

	if _, err := o.d.PostMessage(store.Message{
		ProjectID: p.ID, TicketID: t.ID, Author: "system", Kind: "system",
		Body: fmt.Sprintf("ticket opened: %s (%s, repo %s)", title, kind, repo.Name),
	}); err != nil {
		o.log.Error("post ticket-opened message", "err", err)
	}

	go o.spawnEng(ctx, p, t, repo, repos, pb, fullBrief)
	return t, nil
}

// orderedRepos is a ticket's repo order: primary first, then the rest of
// the project — every repo gets a worktree, used or not, decomposed
// together when the ticket lands.
func orderedRepos(primary store.Repo, repos []store.Repo) []store.Repo {
	ordered := []store.Repo{primary}
	for _, r := range repos {
		if r.ID != primary.ID {
			ordered = append(ordered, r)
		}
	}
	return ordered
}

// plannedTrees computes a ticket's worktree set. Paths are pure functions
// of (root, ticket, repo), so brief-writing and creation agree without
// coordination.
func plannedTrees(root, ticketID string, primary store.Repo, repos []store.Repo) []worktree.Tree {
	var out []worktree.Tree
	for _, r := range orderedRepos(primary, repos) {
		out = append(out, worktree.Tree{
			RepoName: r.Name,
			RepoPath: r.Path,
			Path:     filepath.Join(root, ticketID, r.Name),
			Branch:   worktree.BranchName(ticketID),
		})
	}
	return out
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

// abandonTicket gives up on a failed/blocked ticket for good: unlike retry
// (which opens a fresh one), this just marks it done-with — it stops
// showing up in the board's "needs you" column, which failed/blocked
// tickets otherwise do forever (no age cutoff, unlike done). Fail-closed on
// a worktree with unlanded work, same as project delete and normal
// teardown, unless force.
func (o *Orch) abandonTicket(ticketID string, force bool) (store.Ticket, error) {
	t, err := o.d.Store.TicketByID(ticketID)
	if err != nil {
		return store.Ticket{}, err
	}
	// Terminal() includes failed — exactly the common case for abandon — so
	// checking that directly would reject the whole point of this. Only
	// done/already-abandoned genuinely have nothing left to give up on.
	if t.Status == store.TicketDone || t.Status == store.TicketAbandoned {
		return store.Ticket{}, fmt.Errorf("ticket %s is already %s", t.ID, t.Status)
	}
	o.dropEng(t.ID)
	if !force {
		if trees, _ := o.d.Store.WorktreesForTicket(t.ID); len(trees) > 0 {
			for _, w := range trees {
				if reason := unlandedWork(w.Path); reason != "" {
					return store.Ticket{}, fmt.Errorf("worktree %s has unlanded work (%s) — pass force to abandon anyway and discard it", w.Path, reason)
				}
			}
		}
	}
	o.removeTicketTrees(t.ID, force)
	t.WorktreePath = ""
	t.Status = store.TicketAbandoned
	if err := o.d.UpdateTicket(t); err != nil {
		return store.Ticket{}, err
	}
	o.systemMessage(t.ProjectID, t.ID, "abandoned by the boss")
	return t, nil
}

// spawnEng stands up the ticket's whole worktree set (a tree per project
// repo, env files copied deterministically per the playbook), then starts
// the engineer in the primary repo's tree.
func (o *Orch) spawnEng(ctx context.Context, p store.Project, t store.Ticket, repo store.Repo, repos []store.Repo, pb playbook.Playbook, prompt string) {
	var made []worktree.Tree
	for _, r := range orderedRepos(repo, repos) {
		globs := pb.Recipe(r.Name).EnvGlobsOrDefault()
		tree, err := worktree.Create(o.d.Paths.WorktreesDir(), t.ID, r.Path, r.DefaultBranch, globs)
		if err != nil {
			for _, m := range made { // don't leave a half-built set behind
				worktree.Remove(m.RepoPath, m.Path)
			}
			o.failTicket(t, "worktree setup failed ("+r.Name+"): "+err.Error())
			return
		}
		made = append(made, tree)
		if err := o.d.Store.AddWorktree(store.Worktree{
			TicketID: t.ID, RepoID: r.ID, RepoPath: tree.RepoPath,
			Path: tree.Path, Branch: tree.Branch,
		}); err != nil {
			o.log.Error("record worktree", "err", err)
		}
	}
	t.WorktreePath = made[0].Path // primary — the engineer's cwd
	t.Branch = made[0].Branch
	t.Status = store.TicketRunning
	if err := o.d.UpdateTicket(t); err != nil {
		o.log.Error("update ticket", "err", err)
	}
	names := make([]string, len(made))
	for i, m := range made {
		names[i] = m.RepoName
	}
	o.systemMessage(t.ProjectID, t.ID, fmt.Sprintf("engineer started · branch %s · worktrees: %s", made[0].Branch, strings.Join(names, ", ")))

	sess, err := o.harness.Start(ctx, agent.Spec{
		Model:      o.engModel(t),
		WorkDir:    made[0].Path,
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
		o.systemMessage(t.ProjectID, t.ID, "demo posted — parked for your verification")
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
		o.systemMessage(t.ProjectID, t.ID, "ticket done")
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
	title := "failed"
	if looksLikeNetworkFailure(msg) {
		title = "offline? check VPN/connection, then r to retry"
		msg = "This looks like a connectivity problem (can't reach a host), not something wrong " +
			"with the ticket itself — nothing is lost. Reconnect, then press r to retry.\n\n" + msg
	}
	o.systemMessage(t.ProjectID, t.ID, msg)
	o.fileTicketItem(t, store.ItemFailed, store.TierInterrupt, title, msg)
}

// looksLikeNetworkFailure flags error text matching common
// connectivity-failure signatures (git/curl/DNS wording) — these are
// almost always "you're offline or off-VPN," not a real problem with the
// ticket, so the fix is reconnect-and-retry, not investigation.
func looksLikeNetworkFailure(msg string) bool {
	lower := strings.ToLower(msg)
	for _, sig := range []string{
		"could not resolve host",
		"could not resolve proxy",
		"connection timed out",
		"network is unreachable",
		"connection refused",
		"could not connect to",
		"no route to host",
		"temporary failure in name resolution",
	} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
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
