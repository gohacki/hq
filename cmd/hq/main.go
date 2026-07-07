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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gohacki/hq/internal/agent"
	"github.com/gohacki/hq/internal/agent/claude"
	"github.com/gohacki/hq/internal/agent/pi"
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
		if len(args) > 1 {
			switch args[1] {
			case "run":
				return runDaemon(paths)
			case "stop":
				return stopDaemon(paths)
			case "restart":
				return restartDaemon(paths)
			}
		}
		return fmt.Errorf("usage: hq daemon run|stop|restart")
	case "doctor":
		return runDoctor()
	case "call":
		return runCall(paths, args[1:])
	case "mcp-em":
		return orch.RunEMMCP(paths, args[1:])
	case "em":
		return orch.RunEMCLI(paths, args[1:])
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
  hq daemon stop           stop the daemon (agents resume on next boot)
  hq daemon restart        swap in the current hq binary; sessions resume
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

	cfg, err := paths.LoadConfig()
	if err != nil {
		return err
	}
	d := daemon.New(paths, st, log)
	d.Orch = orch.New(d, harnessByName(cfg.EngHarness, paths, log, "engineers"), harnessByName(cfg.EMHarness, paths, log, "director/EMs"), log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return d.Run(ctx)
}

// harnessByName builds the harness a role runs on, per hq's config
// (config.json: em_harness / eng_harness, default claude). The chat UX is
// identical either way — hq renders every harness's stream natively.
func harnessByName(name string, paths config.Paths, log *slog.Logger, role string) agent.Harness {
	if name == "pi" {
		log.Info("harness", "role", role, "harness", "pi")
		return pi.New(paths.PiSessionsDir())
	}
	log.Info("harness", "role", role, "harness", "claude")
	return claude.New()
}

// stopDaemon SIGTERMs the daemon named by the pid file and waits for it to
// exit. Agent sessions are closed cleanly and resume on the next boot.
func stopDaemon(paths config.Paths) error {
	b, err := os.ReadFile(paths.PIDPath())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("daemon not running")
			return nil
		}
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("bad pid file %s: %w", paths.PIDPath(), err)
	}
	proc, _ := os.FindProcess(pid)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		os.Remove(paths.PIDPath())
		fmt.Println("daemon not running (stale pid file removed)")
		return nil
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			fmt.Println("daemon stopped")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon (pid %d) did not stop within 10s", pid)
}

// restartDaemon is the self-hosting deploy command: stop the old daemon,
// start one from the current binary, and let Reconcile resume every
// engineer session where it left off.
func restartDaemon(paths config.Paths) error {
	if err := stopDaemon(paths); err != nil {
		return err
	}
	cl, err := connect(paths)
	if err != nil {
		return err
	}
	cl.Close()
	fmt.Println("daemon restarted — agent sessions resume automatically")
	return nil
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
	// redial keeps the TUI alive across daemon restarts: wait for whoever is
	// restarting the daemon (hq daemon restart, a deploy) to bring it back
	// before falling back to auto-starting one ourselves — auto-starting too
	// eagerly would race a restart and resurrect the old binary.
	redial := func() (*rpc.Client, error) {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if cl, err := rpc.Dial(paths.SocketPath()); err == nil {
				return cl, nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		return connect(paths)
	}
	return tui.Run(cl, redial)
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
	tools := []struct {
		name, why string
		optional  bool
	}{
		{"claude", "engineer harness (required)", false},
		{"pi", "manager harness for the director/EMs (falls back to claude)", true},
		{"tmux", "live agent sessions (required for chat's v/t — run hq inside it)", false},
		{"no-mistakes", "delivery pipeline (required for no-mistakes projects)", false},
		{"git", "worktrees + everything else", false},
	}
	ok := true
	for _, t := range tools {
		if p, err := exec.LookPath(t.name); err == nil {
			fmt.Printf("  ok  %-12s %s\n", t.name, p)
		} else if t.optional {
			fmt.Printf("  --  %-12s %s\n", t.name, t.why)
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
