// shipyard — a Slack-like TUI for commanding fleets of coding agents.
//
//	shipyard              open the TUI (auto-starts the daemon)
//	shipyard daemon run   run the daemon in the foreground
//	shipyard doctor       check external tool availability
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

	"github.com/gohacki/shipyard/internal/agent/claude"
	"github.com/gohacki/shipyard/internal/config"
	"github.com/gohacki/shipyard/internal/daemon"
	"github.com/gohacki/shipyard/internal/orch"
	"github.com/gohacki/shipyard/internal/rpc"
	"github.com/gohacki/shipyard/internal/store"
	"github.com/gohacki/shipyard/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "shipyard:", err)
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
		return fmt.Errorf("usage: shipyard daemon run")
	case "doctor":
		return runDoctor()
	case "call":
		return runCall(paths, args[1:])
	case "mcp-lead":
		return orch.RunLeadMCP(paths, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

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

// runCall is a scripting/debugging passthrough: `shipyard call <method>
// [json-params]` prints the raw RPC result. Auto-starts the daemon.
func runCall(paths config.Paths, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: shipyard call <method> [json-params]")
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
		{"tmux", "escape hatch (required for `t`)"},
		{"treehouse", "worktree pools (required for crewmates)"},
		{"no-mistakes", "delivery pipeline (required for no-mistakes channels)"},
		{"git", "everything"},
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
