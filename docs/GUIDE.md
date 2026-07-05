# hq user guide

Everything you need to run your department. For rationale see
[SPEC.md](../SPEC.md); for internals see [ARCHITECTURE.md](../ARCHITECTURE.md).

## The mental model

hq is an engineering department where everyone except you is an agent:

| Company thing | hq thing |
|---|---|
| You | the boss — your surface is **My Office** |
| PM | one global agent in the **Conference Room**: intake, creates projects, knows everything |
| A project team | a **project** — one or more git repos, an EM, engineers |
| EM | the agent you brief per project; plans and delegates, never codes |
| Engineer | an autonomous agent working one **ticket** in an isolated git worktree |
| Ticket | unit of work: **build** (ship a change) or **spike** (investigate → report) |
| Team handbook | per-project doc injected into every engineer brief |
| Sitting at someone's desk | **desk visit** — their live Claude session in tmux |

The core promise: **you're involved in planning and verification; everything
else stays out of sight** until it lands in your office queue.

## Install & start

```sh
go build -o ~/.local/bin/hq ./cmd/hq   # from the repo
hq doctor    # checks claude, tmux, treehouse, no-mistakes, git
hq           # opens the TUI from anywhere; auto-starts the daemon
```

Run inside **tmux** for desk visits. Quitting the TUI stops nothing — the
department keeps working; run `hq` again to reattach.

## Creating a project

Open the Conference Room (sidebar) and tell the PM:

> new project beta-os with repos ~/code/beta-os-api and ~/code/beta-os-web,
> delivery no-mistakes, verify before-delivery

Settings (editable later by asking the EM):

- **delivery** — how build tickets land: `no-mistakes` (default: full
  validation pipeline → push → PR → CI) · `direct-pr` · `local-only`.
- **verify** — when engineers hand you a demo: `on-completion` (default) ·
  `before-delivery` · `none`.

Project creation auto-runs an **onboarding spike** per repo that documents
local development in the team handbook — including how to run several dev
servers from different worktrees at once (that's what makes demos work).
Onboarding docs are cached per repo across projects; say "the dev setup
changed" to the EM to refresh one.

## The flow

1. **Brief the EM** in the project chat: "we need rate limiting on the API".
2. For anything non-trivial the EM **proposes a plan** — it lands in My
   Office. Open it: the doc, proposed tickets, open questions. `space`
   toggles tickets in/out, `enter` answers a question, `A` approves (the
   checked tickets spawn), `R` requests changes.
3. Engineers work autonomously. Watch the **board** (`b`) if curious, or a
   ticket's **timeline** (agent chatter collapsed to first lines; `x`
   expands the full chat). Reply in a thread to steer an engineer directly.
4. When an engineer finishes, a **demo** card appears: what changed, exact
   copy-paste test steps, a dev server running from its own worktree with
   the URL. Test it; `a` approves (the engineer completes and its worktree
   is recycled). The server stays up until you approve.
5. Questions, blockers, and failures also queue as cards — answer inline,
   pick an option with `1-9`, `v` to sit at the desk, or `r` to retry a
   failed ticket fresh.

**Bulk intake:** paste a ticket list to an EM ("create a ticket for each"),
or — since agents inherit all your global Claude MCP servers — tell the PM
"pull my open Linear tickets and sort them into projects".

## My Office

Cards in two bands: **needs you now** (plans, questions, options, blockers,
failures) and **when you have a minute** (demos, handbook edits). Per card:

| Key | Does |
|---|---|
| `j` / `k` | move between cards |
| `enter` | open: plan → review screen · options → arm `1-9` · else the thread |
| `a` | approve — demo: tells the engineer to finish · handbook: applies the edit |
| `o` | open the thread behind the card |
| `v` | desk visit |
| `r` | retry a failed ticket (fresh engineer, same brief) |
| `x` | dismiss |

Items resolve themselves when the underlying ticket moves on.

## Presence (`M`)

- 🟢 **available** — notified of interrupts + demos/handbook edits
- 🎧 **heads-down** — notified of interrupts only
- 👀 **review** — notified of everything

Notifications are macOS desktop alerts; the office queue always holds
everything regardless of mode.

## Keybindings (everywhere)

| Key | Does |
|---|---|
| `tab` | cycle sidebar ↔ main/composer |
| `?` or `/help` | help overlay |
| `b` | department board (h/l/j/k + enter) |
| `M` | cycle presence |
| `v` / `t` | desk visit for the selected agent/ticket |
| `e` | edit the team handbook in `$EDITOR` |
| `x` | in a ticket thread: expand/collapse full chat |
| `1-9` | in a spike thread: promote a proposed ticket |
| `esc` | back (thread → project → office) |
| `q` / `ctrl+c` | quit the TUI (department keeps running) |

Composer commands: `/model [em|eng] <sonnet|opus|fable|haiku>` (scope-aware:
project chat = EM, ticket thread = that engineer), `/help`.

## Models

Everyone defaults to **sonnet**. Upgrade/downgrade live — switches resume
the same session, nothing lost: `/model opus`, or just tell the EM
("upgrade yourself to fable", "run the next ticket on opus"). Per-ticket
models via the EM's create_ticket. `HQ_MODEL` overrides the department
default at daemon start.

## Desk visits

`v` opens a tmux window: the agent's session resumed in the full interactive
Claude Code UI (left, on its own model — EMs bring their hq tools) and a
console in its working directory (right). While you're there, headless
supervision pauses; close the window and your next message resumes it.

## The handbook learns

When you state a durable convention ("we never use raw SQL here"), the EM
proposes a handbook edit — a card in your office with the new text; `a`
applies it. Engineers also fix stale "Local development" sections in place
when they trip over them. Over time the handbook becomes your department's
accumulated training.

## CLI reference

```
hq                       open the TUI (auto-starts the daemon)
hq daemon run            run the daemon in the foreground (debugging)
hq doctor                check required external tools
hq call <method> [json]  raw RPC to the daemon (scripting/debugging)
hq help                  short help
```

`hq call` examples:

```sh
hq call projects.list
hq call items.list
hq call tickets.list '{"project_id":"prj_…"}'
hq call model.set '{"project_id":"prj_…","scope":"em","model":"opus"}'
```

## Where things live

| Path | What |
|---|---|
| `~/.local/share/hq/hq.db` | all state (projects, tickets, messages, items, plans) |
| `~/.local/share/hq/projects/<name>/` | handbook.md, plan docs, per-ticket briefs & reports |
| `~/.local/share/hq/onboarding/` | per-repo cached onboarding docs |
| `~/.local/share/hq/logs/daemon.log` | daemon log |
| `~/.treehouse/…` | worktree pools (managed by treehouse) |

Env overrides: `HQ_DATA_DIR`, `HQ_CONFIG_DIR`, `HQ_MODEL` (department-wide
model default).

## Troubleshooting

- **"daemon did not come up"** — check `~/.local/share/hq/logs/daemon.log`.
- **Desk visit errors** — you need a running tmux server (start hq inside
  tmux).
- **Ticket parked after a restart** — daemon restarts park in-flight
  tickets (a card appears). Reply in the thread; the session resumes.
- **"worktree kept (uncommitted changes)"** — hq refuses to recycle a
  worktree holding unlanded work. Inspect it (path in the message), then
  commit/discard, or return it with `treehouse return --force <path>`.
- **Worktree lease fails with "could not resolve host"** — treehouse
  fetches origin when leasing; you're offline/off-VPN. Reconnect and `r`
  retry the ticket.
- **Start fresh** — `pkill -f "hq daemon"`, delete `~/.local/share/hq`.
  Repos are never touched; engineer branches live in your repos' refs.
