package orch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gohacki/shipyard/internal/agent"
	"github.com/gohacki/shipyard/internal/rpc"
	"github.com/gohacki/shipyard/internal/store"
	"github.com/gohacki/shipyard/internal/worktree"
)

type crewRun struct {
	session agent.Session
}

// createTask is the whole deterministic crewmate birth: record → worktree
// lease → brief on disk → headless agent in the worktree.
func (o *Orch) createTask(ctx context.Context, channelID, repoName, kind, title, brief string) (store.Task, error) {
	if kind != "ship" && kind != "scout" {
		return store.Task{}, fmt.Errorf("kind must be ship or scout")
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(brief) == "" {
		return store.Task{}, fmt.Errorf("title and brief are required")
	}
	ch, err := o.d.Store.ChannelByID(channelID)
	if err != nil {
		return store.Task{}, err
	}
	repos, err := o.d.Store.ReposForChannel(channelID)
	if err != nil {
		return store.Task{}, err
	}
	if len(repos) == 0 {
		return store.Task{}, fmt.Errorf("channel #%s has no repos registered", ch.Name)
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
			return store.Task{}, fmt.Errorf("unknown repo %q (channel has: %s)", repoName, strings.Join(names, ", "))
		}
	}

	t := store.Task{
		ID:        store.NewID("tsk"),
		ChannelID: ch.ID,
		RepoID:    repo.ID,
		Kind:      kind,
		Title:     title,
		Status:    store.TaskQueued,
	}
	taskDir := o.d.Paths.TaskDir(ch.Name, t.ID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return store.Task{}, err
	}
	t.BriefPath = filepath.Join(taskDir, "brief.md")
	if kind == "scout" {
		t.ReportPath = filepath.Join(taskDir, "report.md")
	}
	if err := o.d.Store.CreateTask(t); err != nil {
		return store.Task{}, err
	}

	fullBrief := crewBrief(ch, t, repo, brief)
	if err := os.WriteFile(t.BriefPath, []byte(fullBrief), 0o644); err != nil {
		return store.Task{}, err
	}

	if _, err := o.d.PostMessage(store.Message{
		ChannelID: ch.ID, TaskID: t.ID, Author: "system", Kind: "system",
		Body: fmt.Sprintf("task created: %s (%s, repo %s)", title, kind, repo.Name),
	}); err != nil {
		o.log.Error("post task-created message", "err", err)
	}

	go o.spawnCrew(ctx, ch, t, repo, fullBrief)
	return t, nil
}

func (o *Orch) spawnCrew(ctx context.Context, ch store.Channel, t store.Task, repo store.Repo, prompt string) {
	wt, err := worktree.Lease(repo.Path, "shipyard:"+t.ID)
	if err != nil {
		o.failTask(t, "worktree lease failed: "+err.Error())
		return
	}
	t.WorktreePath = wt
	t.Status = store.TaskRunning
	if err := o.d.UpdateTask(t); err != nil {
		o.log.Error("update task", "err", err)
	}

	sess, err := o.harness.Start(ctx, agent.Spec{
		WorkDir:    wt,
		Prompt:     prompt,
		Autonomous: true, // isolated worktree; matches firstmate's crewmate model
	})
	if err != nil {
		o.failTask(t, "agent start failed: "+err.Error())
		return
	}
	o.mu.Lock()
	o.crews[t.ID] = &crewRun{session: sess}
	o.mu.Unlock()
	o.superviseCrew(t, sess)
}

// sendToCrew delivers a message to a live crewmate, resuming its session in
// the worktree if the process is gone (daemon restart, idle reap, escape).
func (o *Orch) sendToCrew(ctx context.Context, t store.Task, body string) error {
	if t.Status.Terminal() {
		return fmt.Errorf("task %s is %s", t.ID, t.Status)
	}
	o.mu.Lock()
	c, ok := o.crews[t.ID]
	o.mu.Unlock()
	if ok {
		if err := c.session.Send(body); err == nil {
			return nil
		}
		o.dropCrew(t.ID)
	}
	if t.SessionID == "" {
		return fmt.Errorf("crewmate has no session to resume")
	}
	if t.WorktreePath == "" {
		return fmt.Errorf("crewmate has no worktree")
	}
	sess, err := o.harness.Start(ctx, agent.Spec{
		WorkDir:         t.WorktreePath,
		Prompt:          body,
		ResumeSessionID: t.SessionID,
		Autonomous:      true,
	})
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.crews[t.ID] = &crewRun{session: sess}
	o.mu.Unlock()
	t.Status = store.TaskRunning
	if err := o.d.UpdateTask(t); err != nil {
		o.log.Error("update task", "err", err)
	}
	go o.superviseCrew(t, sess)
	return nil
}

func (o *Orch) dropCrew(taskID string) {
	o.mu.Lock()
	if c, ok := o.crews[taskID]; ok {
		c.session.Close()
		delete(o.crews, taskID)
	}
	o.mu.Unlock()
}

var (
	reStatusDone    = regexp.MustCompile(`(?m)^STATUS:\s*done\b(.*)$`)
	reStatusBlocked = regexp.MustCompile(`(?m)^STATUS:\s*blocked\b(.*)$`)
	reQuestion      = regexp.MustCompile(`(?m)^QUESTION:\s*(.+)$`)
)

// superviseCrew is the deterministic supervisor for one crewmate process:
// stream text into the thread, classify turn-ends by protocol markers, and
// surface only actionable states to the captain.
func (o *Orch) superviseCrew(t store.Task, sess agent.Session) {
	ch, chErr := o.d.Store.ChannelByID(t.ChannelID)
	if chErr != nil {
		o.log.Error("channel lookup", "err", chErr)
		return
	}
	post := func(kind, body string) {
		if _, err := o.d.PostMessage(store.Message{
			ChannelID: t.ChannelID, TaskID: t.ID, Author: "crew:" + t.ID, Kind: kind, Body: body,
		}); err != nil {
			o.log.Error("post crew message", "err", err)
		}
	}
	refresh := func() {
		if cur, err := o.d.Store.TaskByID(t.ID); err == nil {
			t = cur
		}
	}

	for ev := range sess.Events() {
		switch ev.Kind {
		case agent.EvInit:
			refresh()
			if ev.SessionID != "" && ev.SessionID != t.SessionID {
				t.SessionID = ev.SessionID
				if err := o.d.UpdateTask(t); err != nil {
					o.log.Error("persist crew session", "err", err)
				}
			}
		case agent.EvText:
			post("text", ev.Text)
		case agent.EvResult:
			refresh()
			if t.Status.Terminal() || t.Status == store.TaskAttached {
				continue
			}
			o.classifyTurnEnd(&t, ch, ev, post)
		case agent.EvExited:
			o.mu.Lock()
			delete(o.crews, t.ID)
			o.mu.Unlock()
			refresh()
			if !t.Status.Terminal() && t.Status != store.TaskAttached && t.Status != store.TaskNeedsInput && t.Status != store.TaskBlocked {
				// Died mid-run without a protocol marker.
				t.Status = store.TaskFailed
				if err := o.d.UpdateTask(t); err != nil {
					o.log.Error("update task", "err", err)
				}
				post("system", "crewmate process exited unexpectedly")
				o.d.NotifyNeedsInput(rpc.NeedsInput{ChannelID: t.ChannelID, TaskID: t.ID, Reason: "failed", Summary: t.Title})
			}
			return
		}
	}
}

// classifyTurnEnd maps a finished crewmate turn onto a task status using the
// brief's mandatory protocol markers. Deterministic — no model in the loop.
func (o *Orch) classifyTurnEnd(t *store.Task, ch store.Channel, ev agent.Event, post func(kind, body string)) {
	text := ev.Text
	switch {
	case ev.IsError:
		t.Status = store.TaskFailed
		post("system", "crewmate turn errored: "+text)
	case reQuestion.MatchString(text):
		t.Status = store.TaskNeedsInput
	case reStatusBlocked.MatchString(text):
		t.Status = store.TaskBlocked
	case reStatusDone.MatchString(text):
		t.Status = store.TaskDone
		if t.Kind == "scout" {
			o.postScoutReport(t, post)
		}
	default:
		// Protocol violation: turn ended without a marker. Surface it rather
		// than guessing.
		t.Status = store.TaskNeedsInput
		post("system", "crewmate ended its turn without a STATUS/QUESTION line — reply to steer it")
	}
	if err := o.d.UpdateTask(*t); err != nil {
		o.log.Error("update task", "err", err)
	}
	switch t.Status {
	case store.TaskNeedsInput, store.TaskBlocked, store.TaskFailed:
		reason := map[store.TaskStatus]string{
			store.TaskNeedsInput: "question",
			store.TaskBlocked:    "blocked",
			store.TaskFailed:     "failed",
		}[t.Status]
		o.d.NotifyNeedsInput(rpc.NeedsInput{ChannelID: t.ChannelID, TaskID: t.ID, Reason: reason, Summary: t.Title})
	case store.TaskDone:
		o.d.NotifyNeedsInput(rpc.NeedsInput{ChannelID: t.ChannelID, TaskID: t.ID, Reason: "done", Summary: t.Title})
	}
}

// postScoutReport renders the scout's report file into the thread as a rich
// report message.
func (o *Orch) postScoutReport(t *store.Task, post func(kind, body string)) {
	b, err := os.ReadFile(t.ReportPath)
	if err != nil {
		post("system", "scout finished but report file is missing: "+err.Error())
		return
	}
	post("report", string(b))
}

func (o *Orch) failTask(t store.Task, msg string) {
	t.Status = store.TaskFailed
	if err := o.d.UpdateTask(t); err != nil {
		o.log.Error("update task", "err", err)
	}
	o.systemMessage(t.ChannelID, t.ID, msg)
	o.d.NotifyNeedsInput(rpc.NeedsInput{ChannelID: t.ChannelID, TaskID: t.ID, Reason: "failed", Summary: t.Title})
}

// --- scout → ship handoff ---

// handoff promotes one "Proposed tasks" bullet from a scout report into a new
// ship task, attaching the full report as context.
func (o *Orch) handoff(ctx context.Context, scoutTaskID, proposal string) (store.Task, error) {
	scout, err := o.d.Store.TaskByID(scoutTaskID)
	if err != nil {
		return store.Task{}, err
	}
	if scout.Kind != "scout" || scout.ReportPath == "" {
		return store.Task{}, fmt.Errorf("task %s is not a scout with a report", scoutTaskID)
	}
	report, err := os.ReadFile(scout.ReportPath)
	if err != nil {
		return store.Task{}, err
	}
	repos, err := o.d.Store.ReposForChannel(scout.ChannelID)
	if err != nil {
		return store.Task{}, err
	}
	repoName := ""
	for _, r := range repos {
		if r.ID == scout.RepoID {
			repoName = r.Name
		}
	}
	brief := fmt.Sprintf(`Implement this proposal from scout task %s ("%s"):

%s

The scout's full report follows — it is your primary context.

---

%s`, scout.ID, scout.Title, proposal, string(report))
	return o.createTask(ctx, scout.ChannelID, repoName, "ship", proposal, brief)
}

// ProposedTasks extracts the "## Proposed tasks" bullets from a scout report.
func ProposedTasks(report string) []string {
	var out []string
	inSection := false
	for _, line := range strings.Split(report, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inSection = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")), "proposed tasks")
			continue
		}
		if inSection && strings.HasPrefix(trimmed, "- ") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		}
	}
	return out
}
