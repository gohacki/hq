// hq — an engineering department as a program.
//
//	hq              open the TUI (auto-starts the daemon)
//	hq daemon run   run the daemon in the foreground
//	hq doctor       check external tool availability
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gohacki/hq/internal/agent/claude"
	"github.com/gohacki/hq/internal/config"
	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/orch"
	"github.com/gohacki/hq/internal/rpc"
	"github.com/gohacki/hq/internal/store"
	"github.com/gohacki/hq/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hq:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.Ensure(); err != nil {
		return err
	}

	if len(args) == 0 {
		return runTUI(paths)
	}
	switch args[0] {
	case "daemon":
		if len(args) > 1 && args[1] == "run" {
			return runDaemon(paths)
		}
		return fmt.Errorf("usage: hq daemon run")
	case "doctor":
		return runDoctor()
	case "call":
		return runCall(paths, args[1:])
	case "mcp-em":
		return orch.RunEMMCP(paths, args[1:])
	case "chat-header":
		return runChatHeader(paths, args[1:])
	case "help", "--help", "-h":
		fmt.Print(helpText)
		return nil
	default:
		fmt.Print(helpText)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

const helpText = `hq — an engineering department as a program.

Usage:
  hq                       open the TUI (auto-starts the daemon)
  hq daemon run            run the daemon in the foreground
  hq doctor                check required external tools
  hq call <method> [json]  raw RPC to the daemon (scripting/debugging)
  hq help                  this help

The one-minute tour:
  You are the boss. The ◆ hq director creates projects ("new project myapp
  with repo ~/code/myapp"). Each project has an EM that plans and delegates
  tickets to engineer agents working in isolated git worktrees. The home
  screen is the KANBAN BOARD; everything that needs YOU — plan reviews,
  demos, questions — is pinned in the sidebar's ⚠ NEEDS YOU inbox. Opening
  a ticket is a chat with the EM and its engineer.

In the TUI (vim-native):
  h/l/j/k gg G enter   move / open        a      approve demo/handbook edit
  b            board (home)               v      attach a live session (tmux window)
  / and :      search / command line      M      presence (heads-down/avail/review)
  ?            full help overlay          q      quit (the department keeps working)

Everyone runs sonnet by default; upgrade any agent live with /model or by
asking the EM ("upgrade yourself to opus").

Full guide: docs/GUIDE.md in the repo, or https://github.com/gohacki/hq
`

func runDaemon(paths config.Paths) error {
	if err := os.MkdirAll(paths.LogDir(), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(paths.LogDir(), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(logFile, nil))

	st, err := store.Open(paths.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	d := daemon.New(paths, st, log)
	d.Orch = orch.New(d, claude.New(), log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return d.Run(ctx)
}

// connect dials the daemon, auto-starting it if the socket is dead.
func connect(paths config.Paths) (*rpc.Client, error) {
	if cl, err := rpc.Dial(paths.SocketPath()); err == nil {
		return cl, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive TUI exit
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting daemon: %w", err)
	}
	go cmd.Wait() // reap if it exits while we're alive
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cl, err := rpc.Dial(paths.SocketPath()); err == nil {
			return cl, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon did not come up within 5s (see %s)", filepath.Join(paths.LogDir(), "daemon.log"))
}

func runTUI(paths config.Paths) error {
	cl, err := connect(paths)
	if err != nil {
		return err
	}
	defer cl.Close()
	return tui.Run(cl)
}

// runCall is a scripting/debugging passthrough: `hq call <method>
// [json-params]` prints the raw RPC result. Auto-starts the daemon.
func runCall(paths config.Paths, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: hq call <method> [json-params]")
	}
	cl, err := connect(paths)
	if err != nil {
		return err
	}
	defer cl.Close()
	var params json.RawMessage
	if len(args) > 1 {
		params = json.RawMessage(args[1])
	}
	var out json.RawMessage
	if err := cl.Call(args[0], params, &out); err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func runDoctor() error {
	tools := []struct{ name, why string }{
		{"claude", "agent harness (required)"},
		{"tmux", "live agent sessions (required for chat's v/t — run hq inside it)"},
		{"no-mistakes", "delivery pipeline (required for no-mistakes projects)"},
		{"git", "worktrees + everything else"},
	}
	ok := true
	for _, t := range tools {
		if p, err := exec.LookPath(t.name); err == nil {
			fmt.Printf("  ok  %-12s %s\n", t.name, p)
		} else {
			fmt.Printf("MISS  %-12s %s\n", t.name, t.why)
			ok = false
		}
	}
	if !ok {
		return fmt.Errorf("missing tools")
	}
	return nil
}
