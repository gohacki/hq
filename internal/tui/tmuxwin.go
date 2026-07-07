package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// tmux window management for live agent sessions. Attaching a session opens
// a NEW tmux window (not a pane split): a small info header pane on top and
// the real, unmediated harness CLI below — no terminal emulation layer, so
// no lag and a real hardware cursor. hq's own window is untouched; closing
// the session's window (or the CLI exiting) checks the session back in and
// tmux lands you back on hq. Exactly one attached window is ever open.

// tmuxAvailable reports whether hq itself is running inside tmux — a new
// window only makes sense within an existing tmux session.
func tmuxAvailable() bool {
	return os.Getenv("TMUX") != ""
}

// attachHeaderHeight is how many rows the info header above the CLI gets.
const attachHeaderHeight = 6

// openAttachWindow creates a new tmux window named name running argv in dir,
// with a header pane above it running headerArgv, and switches to it.
// Returns (windowID, cliPaneID).
func openAttachWindow(name, dir string, argv, headerArgv []string) (winID, paneID string, err error) {
	out, err := exec.Command("tmux", "new-window", "-n", name, "-c", dir,
		"-P", "-F", "#{window_id} #{pane_id}", shellJoin(argv)).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("tmux new-window: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return "", "", fmt.Errorf("tmux new-window: unexpected output %q", out)
	}
	winID, paneID = fields[0], fields[1]

	out, err = exec.Command("tmux", "split-window", "-v", "-b",
		"-l", fmt.Sprint(attachHeaderHeight), "-t", paneID,
		"-P", "-F", "#{pane_id}", shellJoin(headerArgv)).CombinedOutput()
	if err != nil {
		// Header is cosmetic — the session still works without it.
		return winID, paneID, nil
	}
	selectPane(paneID) // the header split stole focus — give it back to the CLI
	return winID, paneID, nil
}

// paneAlive reports whether paneID still exists anywhere on the tmux server.
func paneAlive(paneID string) bool {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		return false
	}
	for _, id := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id == paneID {
			return true
		}
	}
	return false
}

// killWindow closes a whole window. Safe to call on an already-gone window.
func killWindow(winID string) {
	if winID == "" {
		return
	}
	_ = exec.Command("tmux", "kill-window", "-t", winID).Run()
}

// selectPane brings paneID into focus. Safe to call on an already-gone pane.
func selectPane(paneID string) {
	_ = exec.Command("tmux", "select-pane", "-t", paneID).Run()
}

// watchAttachPane polls until the CLI pane disappears (the interactive
// session exited or the window was killed), then calls onClosed and returns.
// Meant to run in its own goroutine; onClosed must be safe to call from any
// goroutine and return quickly — it should just be a model.sendMsg call.
func watchAttachPane(paneID string, onClosed func()) {
	for {
		time.Sleep(750 * time.Millisecond)
		if !paneAlive(paneID) {
			onClosed()
			return
		}
	}
}

// shellJoin quotes argv for use as a single shell command string, the form
// tmux's trailing command argument expects.
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
