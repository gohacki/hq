package orch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gohacki/shipyard/internal/store"
)

func tmuxAvailable() error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not installed")
	}
	if os.Getenv("TMUX") == "" {
		// The daemon rarely runs inside tmux; target the client's session via
		// the default server instead. Requires any tmux server to be up.
		if err := exec.Command("tmux", "has-session").Run(); err != nil {
			return fmt.Errorf("no tmux server running — start shipyard inside tmux to use the escape hatch")
		}
	}
	return nil
}

// openEscapeWindow creates the two-pane hatch window: an interactive agent
// session on the left, a console shell on the right, both cwd'd to dir.
// tmuxSession targets the caller's tmux session (the daemon usually runs
// outside tmux, where an untargeted new-window lands in an arbitrary
// session); "" falls back to tmux's default choice.
func (o *Orch) openEscapeWindow(tmuxSession, winName, dir, sessionID, model string, extraArgs ...string) error {
	resume := o.harness.InteractiveCommand(sessionID, model, extraArgs...)
	target := winName
	args := []string{"new-window", "-n", winName, "-c", dir}
	if tmuxSession != "" {
		args = append(args, "-t", tmuxSession+":")
		target = tmuxSession + ":" + winName
	}
	args = append(args, shellJoin(resume))
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-window: %w %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("tmux", "split-window", "-h", "-t", target, "-c", dir).CombinedOutput(); err != nil {
		o.log.Error("escape split failed", "err", err, "out", string(out))
	}
	exec.Command("tmux", "select-pane", "-t", target+".0").Run()
	return nil
}

// leadEscape opens the channel's lead interactively: its session resumed in
// Claude Code on the left, a console in the channel's data dir on the right.
// The headless lead (if warm) is dropped first so one process owns the
// session; the next channel message resumes it headlessly as usual.
func (o *Orch) leadEscape(channelID, tmuxSession string) (string, error) {
	if err := tmuxAvailable(); err != nil {
		return "", err
	}
	ch, err := o.d.Store.ChannelByID(channelID)
	if err != nil {
		return "", err
	}
	if ch.LeadSessionID == "" {
		return "", fmt.Errorf("#%s has no lead session yet — message the channel first", ch.Name)
	}
	o.dropLead(ch.ID)
	winName := "sy-lead-" + ch.Name
	dir := o.d.Paths.ChannelDir(ch.Name)
	// Same MCP tools as the headless lead, so the interactive session can
	// delegate tasks too.
	extra := []string{}
	if mcp := filepath.Join(dir, "mcp.json"); fileExists(mcp) {
		extra = append(extra, "--mcp-config", mcp)
	}
	if err := o.openEscapeWindow(tmuxSession, winName, dir, ch.LeadSessionID, ch.LeadModel, extra...); err != nil {
		return "", err
	}
	return winName, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// escapeHatch opens a tmux window for a task: crewmate resumed interactively
// in Claude Code on the left, an empty shell in the worktree on the right.
// The headless process is closed first so exactly one process owns the
// session; supervision resumes when the window closes.
func (o *Orch) escapeHatch(taskID, tmuxSession string) (string, error) {
	if err := tmuxAvailable(); err != nil {
		return "", err
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
	if err := o.openEscapeWindow(tmuxSession, winName, t.WorktreePath, t.SessionID, o.taskModel(t)); err != nil {
		t.Status = prev
		o.d.UpdateTask(t)
		return "", err
	}

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
