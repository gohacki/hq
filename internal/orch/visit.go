package orch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gohacki/hq/internal/store"
)

// Desk visits: sit down at an agent's desk — a tmux window with their Claude
// session resumed interactively on the left and a console shell on the right.

func tmuxAvailable() error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not installed")
	}
	if os.Getenv("TMUX") == "" {
		// The daemon rarely runs inside tmux; target the client's session via
		// the default server instead. Requires any tmux server to be up.
		if err := exec.Command("tmux", "has-session").Run(); err != nil {
			return fmt.Errorf("no tmux server running — start hq inside tmux to use desk visits")
		}
	}
	return nil
}

// openVisitWindow creates the two-pane visit window. tmuxSession targets the
// caller's tmux session (the daemon usually runs outside tmux, where an
// untargeted new-window lands in an arbitrary session).
func (o *Orch) openVisitWindow(tmuxSession, winName, dir, sessionID, model string, extraArgs ...string) error {
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
		o.log.Error("visit split failed", "err", err, "out", string(out))
	}
	exec.Command("tmux", "select-pane", "-t", target+".0").Run()
	return nil
}

// visitEM opens the project's EM (or the PM) interactively: its session
// resumed in Claude Code on the left, a console in the project's data dir on
// the right. The headless process (if warm) is dropped first so one process
// owns the session; the next project message resumes it headlessly as usual.
func (o *Orch) visitEM(projectID, tmuxSession string) (string, error) {
	if err := tmuxAvailable(); err != nil {
		return "", err
	}
	p, err := o.d.Store.ProjectByID(projectID)
	if err != nil {
		return "", err
	}
	if p.EMSessionID == "" {
		return "", fmt.Errorf("%s has no manager session yet — message the project first", p.Name)
	}
	o.dropEM(p.ID)
	winName := "hq-em-" + p.Name
	dir := o.d.Paths.ProjectDir(p.Name)
	// Same MCP tools as the headless EM, so the interactive session can
	// delegate tickets too.
	extra := []string{}
	if mcp := filepath.Join(dir, "mcp.json"); fileExists(mcp) {
		extra = append(extra, "--mcp-config", mcp)
	}
	if err := o.openVisitWindow(tmuxSession, winName, dir, p.EMSessionID, p.EMModel, extra...); err != nil {
		return "", err
	}
	return winName, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// visitEngineer opens a ticket's engineer interactively in its worktree. The
// headless process is closed first so exactly one process owns the session;
// supervision resumes when the window closes.
func (o *Orch) visitEngineer(ticketID, tmuxSession string) (string, error) {
	if err := tmuxAvailable(); err != nil {
		return "", err
	}
	t, err := o.d.Store.TicketByID(ticketID)
	if err != nil {
		return "", err
	}
	if t.SessionID == "" || t.WorktreePath == "" {
		return "", fmt.Errorf("ticket has no live session/worktree to visit")
	}

	// Hand the session over: kill headless, mark visiting.
	o.dropEng(t.ID)
	prev := t.Status
	t.Status = store.TicketVisiting
	if err := o.d.UpdateTicket(t); err != nil {
		return "", err
	}

	winName := "hq-" + strings.TrimPrefix(t.ID, "tkt_")
	if err := o.openVisitWindow(tmuxSession, winName, t.WorktreePath, t.SessionID, o.engModel(t)); err != nil {
		t.Status = prev
		o.d.UpdateTicket(t)
		return "", err
	}

	go o.watchVisitWindow(t, winName, prev)
	return winName, nil
}

// watchVisitWindow polls for the tmux window; when it's gone the boss is
// done steering and headless supervision resumes on the same session. A
// ticket that was already terminal before the visit returns to that status
// instead of being parked.
func (o *Orch) watchVisitWindow(t store.Ticket, winName string, prev store.TicketStatus) {
	for {
		time.Sleep(3 * time.Second)
		out, err := exec.Command("tmux", "list-windows", "-a", "-F", "#{window_name}").Output()
		if err != nil || !strings.Contains(string(out), winName) {
			break
		}
	}
	cur, err := o.d.Store.TicketByID(t.ID)
	if err != nil || cur.Status != store.TicketVisiting {
		return // ticket moved on while visiting
	}
	if prev.Terminal() {
		cur.Status = prev
		if err := o.d.UpdateTicket(cur); err != nil {
			o.log.Error("update ticket", "err", err)
		}
		return
	}
	cur.Status = store.TicketNeedsInput
	if err := o.d.UpdateTicket(cur); err != nil {
		o.log.Error("update ticket", "err", err)
	}
	o.systemMessage(cur.ProjectID, cur.ID,
		"desk visit ended — reply in this thread to resume headless supervision")
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
