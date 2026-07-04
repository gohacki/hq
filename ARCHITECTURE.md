# shipyard — architecture

Go, single binary, three moving parts: **CLI/TUI**, **daemon**, **agent
subprocesses**. The daemon is the only writer of state; the TUI is a thin
client.

```
┌────────────┐   unix socket (JSON-RPC + event stream)   ┌──────────────────┐
│ shipyard   │◄──────────────────────────────────────────►│ shipyard daemon  │
│ (Bubble    │                                            │  - SQLite state  │
│  Tea TUI)  │                                            │  - supervisor    │
└────────────┘                                            │  - agent runner  │
                                                          └───────┬──────────┘
                                                   spawns/owns    │ stdio
                                            ┌─────────────────────┼─────────────┐
                                            ▼                     ▼             ▼
                                     lead agent (per      crewmate (per    treehouse /
                                     channel, on-demand,  task, headless   no-mistakes /
                                     headless claude)     claude in        tmux (CLIs)
                                                          worktree)
```

## Directory layout

```
cmd/shipyard/          main; subcommands: (default) tui, daemon, channel, task, doctor
internal/config/       paths (~/.config/shipyard, ~/.local/share/shipyard), settings
internal/store/        SQLite schema + queries (channels, repos, tasks, messages, reads)
internal/rpc/          JSON-RPC over unix socket: requests + server-push events
internal/daemon/       daemon core: lifecycle, supervisor, event bus
internal/agent/        Harness interface; claude/ adapter (stream-json, resume)
internal/lead/         lead agent runner: system prompt, MCP tool server
internal/crew/         crewmate lifecycle: brief building, spawn, supervise, teardown
internal/worktree/     treehouse CLI wrapper (get --lease / return / status)
internal/delivery/     delivery modes; no-mistakes gate finding relay
internal/escape/       tmux escape hatch (window + two panes)
internal/tui/          Bubble Tea app: sidebar, channel, thread, composer, forms
```

## State

SQLite at `~/.local/share/shipyard/shipyard.db` (WAL). Daemon is sole writer.

- `channels(id, name, delivery, instructions_path, lead_session_id, created_at)`
- `repos(id, channel_id, name, path, default_branch)`
- `tasks(id, channel_id, repo_id, kind ship|scout, title, status, branch,
  worktree_path, session_id, brief_path, report_path, created_at, updated_at)`
  - status: `queued|running|needs-input|blocked|delivering|done|failed|abandoned`
- `messages(id, channel_id, task_id NULL, author kind+name, body, kind
  text|report|gate|system, created_at)`
- `reads(scope_id, last_read_message_id)` — unread badges are derived.

Channel data dir `~/.local/share/shipyard/channels/<name>/` holds
`instructions.md`, per-task `tasks/<id>/{brief.md,report.md}`, lead session
metadata. Human-readable on purpose.

## Daemon

- `shipyard daemon run` in foreground; the CLI auto-spawns it detached when
  the socket (`~/.local/share/shipyard/daemon.sock`) is dead. PID + version
  handshake; stale-socket cleanup.
- **Event bus**: every state mutation emits an event; TUI subscribes over the
  socket (`events.subscribe`) and re-renders. Notifications derive from the
  same events.
- **Supervisor** (deterministic, zero-token): watches crewmate processes and
  their stream-json output. Classifies turn-ends: progress (absorb), question
  / gate / blocked (→ `needs-input`, notify, optionally wake lead), exit
  (success → delivery/teardown path; failure → surface). Restart-proof: on
  boot, reconcile db against live processes, treehouse status, and git.

## Agent layer

`internal/agent.Harness` interface:

```go
type Harness interface {
    Start(ctx, Spec) (Session, error)   // Spec: cwd, systemPrompt, mcpServers, resumeID
    // Session: Send(msg), Events() <-chan Event, Interrupt(), SessionID()
}
```

Claude adapter runs `claude -p --output-format stream-json --input-format
stream-json --permission-mode acceptEdits ...` (crewmates:
`--dangerously-skip-permissions` inside their isolated worktree, matching
firstmate's autonomy model). Session IDs persist in the db → `--resume` gives
leads durable memory and powers the escape hatch.

## Lead ↔ daemon tools

The daemon exposes an MCP stdio server (`shipyard mcp-lead --channel <id>`,
spawned per lead) with tools: `create_task`, `list_tasks`, `get_task`,
`message_task`, `cancel_task`, `read_report`, `read_instructions`,
`edit_instructions`, `list_repos`, `channel_settings`. The home channel's
assistant gets `create_channel`, `add_repo`, `set_delivery` instead. Tool
calls go straight back into the daemon over the same unix socket — the lead
never runs shell commands against shipyard state.

## Crewmate lifecycle

1. Lead calls `create_task` → daemon leases a worktree: `treehouse get
   --lease --lease-holder shipyard:<task-id>` in the repo (pool configured by
   the repo's `treehouse.toml`, seeded on channel creation).
2. Brief is rendered (task, channel instructions, delivery-mode contract,
   report contract for scouts) to `tasks/<id>/brief.md`; crewmate spawns
   headless in the worktree.
3. Supervisor streams assistant text into the task thread as messages; tool
   noise is summarized, not mirrored.
4. Ship + no-mistakes: crewmate commits on a feature branch and drives
   `no-mistakes axi run`; `ask-user` gate findings post to the thread and
   block on your reply.
5. Done: PR link (or merge/report) posted; worktree returned via `treehouse
   return --force` **only after landed-work checks pass** (firstmate's
   fail-closed teardown rule).

## Escape hatch

`t` on a thread → `tmux new-window -n sy:<task>` with left pane `claude
--resume <session-id>` (cwd = worktree) and right pane `$SHELL` (cwd =
worktree). Daemon marks the task `attached` and pauses headless sends; when
the window closes (polled via tmux), supervision resumes on the same session.
Requires running inside tmux; the keybinding errors gracefully otherwise.

## Testing

- Store + supervisor: pure Go unit tests (supervisor classification is
  table-driven — the part firstmate could never test).
- Agent adapter: golden stream-json fixtures; a `fakeharness` binary for e2e.
- TUI: teatest golden-frame tests.
- Repo gates itself with no-mistakes once bootstrapped.
