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
                                     PM / EMs (per         engineers (per   treehouse /
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
internal/orch/        judgment layer: PM/EM lifecycle (orch.go), engineers (eng.go),
                      plans/ask_boss/handbook (plans.go), prompts.go, mcpcmd.go,
                      onboarding.go, visit.go, teardown.go
internal/mcp/         minimal MCP stdio server (initialize, tools/list, tools/call)
internal/worktree/    treehouse CLI wrapper (get --lease / return)
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

`agent.Harness`/`Session` unchanged from v1: headless
`claude -p --input-format stream-json --output-format stream-json`,
`--append-system-prompt` for PM/EM roles, `--resume` for durable memory and
desk visits, `--model` per spec (default sonnet; `fable`→`claude-fable-5`).
All agents run `--dangerously-skip-permissions`: engineers are isolated in
worktrees; the PM/EM delegate-don't-do rule is prompt-enforced.

## Orchestration

- **PM/EM**: same lifecycle (on-demand, 15-min idle reap, resume-on-wake).
  The PM is the Conference Room's EM with a different prompt + extra tools.
  MCP config points at `hq mcp-em --project <id> [--pm]`, whose tools call
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

## TUI

One Bubble Tea model with four screens (office/chat/plan/board) + help
overlay. Sidebar: my office (interrupt badge), board, conference room,
projects (ticket threads nest under the open one; staff = EM + live
engineers, enter = desk visit). Ticket chats render as timelines (engineer
text collapsed to first lines; `x` expands). Presence cycles with `M`;
notifications are osascript desktop alerts gated by tier.

## Testing

- store: schema round-trips, items lifecycle, plans, settings.
- daemon e2e: real socket — boot, conference room, project create,
  messages/events, items file→auto-resolve, presence validation.
- orch: marker classification, proposed-ticket parsing, teardown safety
  (unlandedWork against real git repos).
- Live verification: fake repos + Sonnet fleet driven over tmux.
