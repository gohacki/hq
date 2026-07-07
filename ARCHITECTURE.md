# hq — architecture

Go, single binary, three moving parts: **CLI/TUI**, **daemon**, **agent
subprocesses**. The daemon is the only writer of state; the TUI is a thin
client rendering My Office, the board, and project chats from RPC + events.

```
┌────────────┐   unix socket (JSON-RPC + event stream)   ┌──────────────────┐
│ hq         │◄──────────────────────────────────────────►│ hq daemon        │
│ (Bubble    │                                            │  - SQLite state  │
│  Tea TUI)  │                                            │  - items engine  │
└────────────┘                                            │  - supervisor    │
                                                          └───────┬──────────┘
                                                   spawns/owns    │ stdio
                                            ┌─────────────────────┼─────────────┐
                                            ▼                     ▼             ▼
                              director / EMs (per        engineers (per   treehouse /
                                     project, on-demand,   ticket, headless no-mistakes /
                                     headless claude)      claude in        tmux (CLIs)
                                                           worktree)
```

## Directory layout

```
cmd/hq/               main; subcommands: (default) tui, daemon, doctor, call, mcp-em
internal/config/      paths (~/.config/hq, ~/.local/share/hq), env overrides
internal/store/       SQLite: projects, repos, tickets, messages, reads, items, plans, settings
internal/rpc/         JSON-RPC over unix socket: requests + server-push events
internal/daemon/      daemon core: lifecycle, project creation, items engine, presence
internal/agent/       Harness interface; claude/ adapter (stream-json, resume, models)
internal/orch/        judgment layer: director/EM lifecycle (orch.go), engineers (eng.go),
                      plans/ask_boss/handbook (plans.go), prompts.go, mcpcmd.go,
                      onboarding.go, visit.go, teardown.go
internal/mcp/         minimal MCP stdio server (initialize, tools/list, tools/call)
internal/worktree/    native git worktrees: one set per ticket (a tree per project
                      repo) under <data>/worktrees/<ticket>/<repo>, branch hq/<ticket>,
                      env-file copy; refs shared with the user's repo
internal/playbook/    per-project SDLC capture: playbook.md (prose) + playbook.json
                      (per-repo machine recipe), written by the EM's setup interview
internal/tui/         Bubble Tea app: tui.go (model/update), keys.go, sidebar.go,
                      office.go (+plan review), board.go, views.go, help.go
```

## State

SQLite at `~/.local/share/hq/hq.db` (WAL). Daemon is sole writer. Tables:
`projects` (delivery/verify/models/handbook/EM session), `repos`, `tickets`
(kind build|spike, status, model, worktree, session), `messages`, `reads`,
`items` (the office queue: kind, tier, resolved_at), `plans`, `settings`
(presence). Project data dirs are human-readable:
`projects/<name>/{handbook.md, mcp.json, plan-*.md, tickets/<id>/{brief.md,report.md}}`.

## Items engine (My Office)

Every actionable state files an `items` row and publishes `item.new`:
questions/options (`ask.boss` or engineer `QUESTION:`), plans
(`plan.propose`), demos (engineer `DEMO:`), blocked/failed, handbook
proposals. `UpdateTicket` auto-resolves a ticket's open items when it
returns to running/delivering; office actions resolve explicitly
(`item.resolve`, `plan.approve`, `handbook.apply`). Tier drives
notification gating against the presence setting; the queue is durable.

## Agent layer

Two harnesses behind the same `agent.Harness`/`Session` interface:

- **Engineers — Claude Code** (`internal/agent/claude`): headless
  `claude -p --input-format stream-json --output-format stream-json`,
  `--resume` for durable memory and desk visits, `--model` per spec
  (default sonnet; `fable`→`claude-fable-5`). Runs
  `--dangerously-skip-permissions`: engineers are isolated in worktrees.
- **Director/EMs — pi** (`internal/agent/pi`): the open-source pi coding
  agent in RPC mode (`pi --mode rpc`), a subprocess speaking JSONL
  commands/events over stdio — structured events, no PTY. Session identity
  is the pi session file under `<data>/pi-sessions/`, resumed with
  `--session`; mid-turn sends queue as steering. pi has no MCP, so the EM
  tool set is served as the `hq em` CLI (same tools as `hq mcp-em`, same
  daemon RPCs) and documented in the role prompt. Picked at daemon boot
  when `pi` is on PATH (override: `HQ_EM_HARNESS=pi|claude`); falls back to
  claude, and the delegate-don't-do rule stays prompt-enforced either way.

## Orchestration

- **Director/EM**: same lifecycle (on-demand, 15-min idle reap, resume-on-wake).
  The director is the home room's EM with a different prompt + extra tools.
  MCP config points at `hq mcp-em --project <id> [--director]`, whose tools call
  back into the daemon over the socket.
- **Engineers**: spawn = ticket row → treehouse lease (fetches origin;
  fail-closed) → brief file → headless session. Supervision classifies
  turn-ends by mandatory markers (`DEMO:`/`STATUS:`/`QUESTION:`) —
  deterministic, no model in the loop — and files office items. Done →
  worktree auto-return (kept if unlanded work). Failed → retry re-opens
  with the same brief.
- **Plans**: `plan.propose` → plans row + office item; `plan.approve`
  spawns accepted tickets and messages the EM the boss's decisions/answers;
  `plan.reject` messages the note back.
- **Onboarding docs**: per-repo cache under `onboarding/`; seeded on
  project creation (spike if cache miss), `onboarding.refresh` replaces.
- **Restarts (self-hosting loop)**: `hq daemon stop|restart` signal the pid
  file; shutdown closes agent sessions cleanly (`Orchestrator.Shutdown`).
  Boot `Reconcile` resumes running/delivering engineers in place
  (`--resume` + take-stock nudge); tickets with no session, or checked out
  for a desk visit, are parked as needs-input instead. The TUI survives the
  restart: on socket drop it redials (waiting out the restart before
  auto-starting a daemon itself) and resubscribes.

## TUI

One Bubble Tea model with three screens (board/chat/plan) + help overlay
and the attach picker. The board (kanban) is home, scoped by the sidebar's
project rows. Sidebar sections: ⚠ needs-you inbox (open items, one-key
a/r/d), projects (tickets nest under the focused one), staff (EM + live
engineers). Vim layer in `internal/tui/vim.go`: gg/G, `/` search with n/N,
`:` command line, h/l focus movement. Presence cycles with `M`;
notifications are osascript desktop alerts gated by tier.

Chat is the hq-rendered thread (`chatContent`): boss/EM/engineer messages
with `system`-kind rows drawn as one-line interleaved event markers, a
two-line metadata header for tickets, and a pending-decision banner above
the composer (approve/retry/dismiss/options — same actions as the inbox).
Sessions stay headless; messages route through `message.send` as always.

Attaching (`v`) opens the picker (EM + live engineers), then
`session.checkout` hands over the argv+dir and `internal/tui/tmuxwin.go`
opens a NEW tmux window: an info header pane (`hq chat-header`) above the
real interactive harness CLI. A background watcher (`watchAttachPane`)
polls for the CLI pane dying — the human exited or killed the window — and
checks the session back in (`session.checkin`), resuming headless
supervision on the same session id. Exactly one attached window at a time;
attaching another closes (and checks in) the previous one first. Requires
hq to be running inside tmux; outside tmux everything but attach works.

## Testing

- store: schema round-trips, items lifecycle, plans, settings.
- daemon e2e: real socket — boot, director room, project create,
  messages/events, items file→auto-resolve, presence validation.
- orch: marker classification, proposed-ticket parsing, teardown safety
  (unlandedWork against real git repos).
- Live verification: fake repos + Sonnet fleet driven over tmux.
