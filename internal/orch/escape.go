package orch

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gohacki/shipyard/internal/store"
)

// escapeHatch opens a tmux window for a task: crewmate resumed interactively
// in Claude Code on the left, an empty shell in the worktree on the right.
// The headless process is closed first so exactly one process owns the
// session; supervision resumes when the window closes.
func (o *Orch) escapeHatch(taskID string) (string, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return "", fmt.Errorf("tmux not installed")
	}
	if os.Getenv("TMUX") == "" {
		// The daemon rarely runs inside tmux; target the client's session via
		// the default server instead. Requires any tmux server to be up.
		if err := exec.Command("tmux", "has-session").Run(); err != nil {
			return "", fmt.Errorf("no tmux server running — start shipyard inside tmux to use the escape hatch")
		}
	}
	t, err := o.d.Store.TaskByID(taskID)
	if err != nil {
		return "", err
	}
	if t.SessionID == "" || t.WorktreePath == "" {
		return "", fmt.Errorf("task has no live session/worktree to attach")
	}

	// Hand the session over: kill headless, mark attached.
	o.dropCrew(t.ID)
	prev := t.Status
	t.Status = store.TaskAttached
	if err := o.d.UpdateTask(t); err != nil {
		return "", err
	}

	winName := "sy-" + strings.TrimPrefix(t.ID, "tsk_")
	resume := o.harness.InteractiveCommand(t.SessionID)
	// Left pane: interactive resume. Right pane: shell in the worktree.
	cmd := exec.Command("tmux", "new-window", "-n", winName, "-c", t.WorktreePath, shellJoin(resume))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Status = prev
		o.d.UpdateTask(t)
		return "", fmt.Errorf("tmux new-window: %w %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("tmux", "split-window", "-h", "-t", winName, "-c", t.WorktreePath).CombinedOutput(); err != nil {
		o.log.Error("escape split failed", "err", err, "out", string(out))
	}
	exec.Command("tmux", "select-pane", "-t", winName+".0").Run()

	go o.watchEscapeWindow(t, winName, prev)
	return winName, nil
}

// watchEscapeWindow polls for the tmux window; when it's gone the captain is
// done steering and headless supervision resumes on the same session. A task
// that was already terminal before the escape returns to that status instead
// of being parked.
func (o *Orch) watchEscapeWindow(t store.Task, winName string, prev store.TaskStatus) {
	for {
		time.Sleep(3 * time.Second)
		out, err := exec.Command("tmux", "list-windows", "-a", "-F", "#{window_name}").Output()
		if err != nil || !strings.Contains(string(out), winName) {
			break
		}
	}
	cur, err := o.d.Store.TaskByID(t.ID)
	if err != nil || cur.Status != store.TaskAttached {
		return // task moved on while attached (e.g. captain abandoned it)
	}
	if prev.Terminal() {
		cur.Status = prev
		if err := o.d.UpdateTask(cur); err != nil {
			o.log.Error("update task", "err", err)
		}
		return
	}
	cur.Status = store.TaskNeedsInput
	if err := o.d.UpdateTask(cur); err != nil {
		o.log.Error("update task", "err", err)
	}
	o.systemMessage(cur.ChannelID, cur.ID,
		"escape-hatch window closed — reply in this thread to resume headless supervision")
}

func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t\"'$") {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}
