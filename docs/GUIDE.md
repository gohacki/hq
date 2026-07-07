# hq user guide

Everything you need to run your department. For rationale see
[SPEC.md](../SPEC.md); for internals see [ARCHITECTURE.md](../ARCHITECTURE.md).

## The mental model

hq is an engineering department where everyone except you is an agent:

| Company thing | hq thing |
|---|---|
| You | the boss — your surface is the **kanban board** + the **⚠ needs-you inbox** |
| Intake | **◆ hq** in the sidebar — an EM that creates projects and knows every project's state |
| A project team | a **project** — one or more git repos, an EM, engineers |
| EM | the agent you brief per project; plans and delegates, never codes |
| Engineer | an autonomous agent working one **ticket** in an isolated git worktree |
| Ticket | unit of work: **build** (ship a change) or **spike** (investigate → report) |
| Team handbook | per-project doc injected into every engineer brief |
| Talking to an agent | **chat** — the ticket/project thread; `v` attaches the real Claude Code CLI in a new tmux window |

The core promise: **you're involved in planning and verification; everything
else stays out of sight** until it lands in your inbox.

## Install & start

```sh
go build -o ~/.local/bin/hq ./cmd/hq   # from the repo
hq doctor    # checks claude, tmux, treehouse, no-mistakes, git
hq           # opens the TUI from anywhere; auto-starts the daemon
```

Run **inside tmux** — attaching a live session (`v`) opens a new tmux
window; outside tmux everything else still works (chat, board, plans).
Quitting the TUI stops nothing — the department keeps working; run `hq`
again to reattach.

## The screen

```
┌─SIDEBAR──────────┬─BOARD (home) ─────────────────────────────┐
│ — needs you (2) —│ backlog │ in prog │ verify │ needs │ done │
│ ❓ question — …   │ ┌─────┐ │ ┌─────┐ │ ┌────┐ │  you  │      │
│ 🖥 demo ready — … │ │t-21 │ │ │t-14 │ │ │t-08│ │       │      │
│ — projects —     │ └─────┘ │ └─────┘ │ │:3001││       │      │
│ ▤ all projects   │         │         │ └────┘ │       │      │
│ ◆ hq             │         │         │        │       │      │
│ # os           3 │         │         │        │       │      │
│ # maestro      1 │         │         │        │       │      │
│ — staff —        │         │         │        │       │      │
│ ◉ EM · sonnet    │         │         │        │       │      │
│ ● add ping button│         │         │        │       │      │
└──────────────────┴─────────┴─────────┴────────┴───────┴──────┘
```

- **Board** (home): columns are the ticket lifecycle. enter on a project row
  scopes it to that project; `▤ all projects` (or `esc`) shows everything.
  Verify cards carry the engineer's **dev-server URL**.
- **Inbox** (sidebar top): everything awaiting you — plans, questions,
  demos, blockers. `enter` opens it, `a`/`r`/`d` act on it right there.
- **Chat**: enter on a ticket (board card or sidebar row) opens its thread —
  you, the EM, and the engineer, with dim `· event` lines (started, demo
  posted, done) woven in. The header shows status · model · worktree ·
  dev URL · age.

## Creating a project

Open **◆ hq** (sidebar) and tell the director:

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

Then open the project's chat: the EM runs a one-time **setup interview**,
capturing the whole lifecycle into the project **playbook** (`p` to read
it) — per repo: env files to copy into every worktree, install command,
workspace-verify command, dev-server command + port strategy, a local
mirror of the CI pipeline (it reads your `.gitlab-ci.yml` itself); plus
how you want verification to work and a step-by-step walkthrough of the
delivery gate (it runs `no-mistakes --help` and explains every gate
between "engineer finished" and "merged to main / deployed"). After the
interview the project is a **ticket machine**: throw tickets in, watch
5–15 run at once.

## Worktrees (per ticket, per repo)

Every ticket owns an isolated **native git worktree of every project
repo** (`~/.local/share/hq/worktrees/<ticket>/<repo>`), all on branch
`hq/<ticket>`, env files pre-copied per the playbook. Native worktrees
share refs with your repo, so an engineer's branch is instantly visible in
`~/code/<repo>` — merge whenever. When the ticket lands, the whole set is
decomposed (fail-closed if any tree holds uncommitted work); unused
sibling trees cost nothing. Engineers verify their workspace (install +
verify commands) as phase 0 and report playbook drift so the EM fixes the
recipe.

## The flow

1. **Brief the EM** in the project chat: "we need rate limiting on the API".
   For anything non-trivial it **grills you first** — one question at a
   time, each with a recommended answer, until shared understanding (facts
   it can find in the repo it looks up itself; say "no grilling" to skip).
2. For anything non-trivial the EM **proposes a plan** — it lands in your
   inbox. Open it: the doc, proposed tickets, open questions. `space`
   toggles tickets in/out, `enter` answers a question, `A` approves (the
   checked tickets spawn), `R` requests changes.
3. Engineers work autonomously. Watch the **board** fill in, or open a
   ticket's chat to read along; `v` attaches the engineer's actual live
   Claude Code session in a new tmux window when you want hands on.
4. When an engineer finishes, the ticket parks in **verify** with a demo:
   what changed, exact copy-paste test steps, a dev server running from its
   own worktree with the URL (on the card and in the chat banner). Test it;
   `a` approves (the engineer completes and its worktree is recycled). The
   server stays up until you approve.
5. Questions, blockers, and failures land in the inbox and the board's
   **needs you** column — reply in the thread, pick an option with `1-9`,
   or `r` to retry a failed ticket fresh.

**Bulk intake:** paste a ticket list to an EM ("create a ticket for each"),
or — since agents inherit all your global Claude MCP servers — tell the
director "pull my open Linear tickets and sort them into projects".

## The chat banner

When the open chat has a decision waiting, a banner sits above the composer:

| Key | Does |
|---|---|
| `a` | approve — demo: tells the engineer to finish · handbook: applies the edit |
| `r` | retry a failed/blocked ticket (fresh engineer, same brief) |
| `d` | dismiss — failed/blocked tickets are abandoned too |
| `1-9` | answer an options question (or promote a spike proposal) |
| `P` | open a proposed plan's review screen |

The same `a`/`r`/`d` work on inbox rows in the sidebar. Items resolve
themselves when the underlying ticket moves on.

## Presence (`M`)

- 🟢 **available** — notified of interrupts + demos/handbook edits
- 🎧 **heads-down** — notified of interrupts only
- 👀 **review** — notified of everything

Notifications are macOS desktop alerts; the inbox always holds everything
regardless of mode.

## Keybindings (everywhere)

| Key | Does |
|---|---|
| `h` / `l` | move focus sidebar ↔ main (board uses them for columns first — `h` past the leftmost column reaches the sidebar) |
| `tab` | cycle sidebar ↔ main/composer |
| `gg` / `G` | top / bottom of the focused list (or the chat timeline) |
| `ctrl+f/b`, `ctrl+d/u` | page / half-page scroll |
| `i` | insert mode — focus the composer in a chat (`esc` leaves) |
| `/text` | search the focused list (sidebar, plan, board); `n`/`N` repeat |
| `:` | command line — `:q` `:help` `:board` `:hq` `:model …` `:presence [mode]` `:handbook` |
| `?` or `/help` | help overlay |
| `b` | back to the board (`0`/`$` first/last column, enter opens) |
| `M` | cycle presence |
| `v` / `t` | attach picker — open the EM's or an engineer's live session in a new tmux window |
| `p` | read the focused project's playbook (lifecycle doc + machine recipe) |
| `e` | edit the team handbook in `$EDITOR` |
| `x` | in a ticket chat: expand/collapse engineer chatter |
| `esc` | back out, always landing on the sidebar cursor: composer → sidebar · board cards → sidebar · sidebar → project board unscopes to all projects · chat → that project's board |
| `q` / `:q` / `ctrl+c` | quit the TUI (department keeps running) |

Composer commands: `/model [em|eng] <sonnet|opus|fable|haiku>` (scope-aware:
project chat = EM, ticket thread = that engineer), `/help`. The same
commands work from the `:` command line outside the composer.

## Models

Everyone defaults to **sonnet**. Upgrade/downgrade live — switches resume
the same session, nothing lost: `/model opus`, or just tell the EM
("upgrade yourself to fable", "run the next ticket on opus"). Per-ticket
models via the EM's create_ticket. `HQ_MODEL` overrides the department
default at daemon start.

## Live sessions (`v`)

The chat thread is hq-rendered, but the agent underneath is a real Claude
Code session — `v` opens the **attach picker** (the project's EM and every
live engineer), and picking one opens that actual CLI in a **new tmux
window** with an info header: zero lag, a real cursor, nothing emulated.
Headless supervision pauses while you're attached; close the window (or
exit the CLI) to hand the session back — same session id, nothing lost.
Requires hq to be running inside tmux.

## The handbook learns

When you state a durable convention ("we never use raw SQL here"), the EM
proposes a handbook edit — it lands in your inbox with the new text; `a`
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
- **"run hq inside tmux to open live sessions"** — attaching opens a tmux
  window; everything else works outside tmux.
- **Daemon restarts** — running engineers are resumed automatically at the
  next boot (same session, same worktree, a take-stock nudge). A ticket is
  only parked (inbox item, reply-to-resume) when there is nothing to
  resume — no session yet, or it was checked out for a desk visit.
  `hq daemon restart` is the self-hosting deploy command: rebuild, restart,
  everything resumes and the TUI reconnects on its own.
- **"worktree kept (uncommitted changes)"** — hq refuses to recycle a
  worktree holding unlanded work. Inspect it (path in the message), then
  commit/discard, or return it with `treehouse return --force <path>`.
- **Worktree lease fails with "could not resolve host"** — treehouse
  fetches origin when leasing; you're offline/off-VPN. Reconnect and `r`
  retry the ticket.
- **Start fresh** — `pkill -f "hq daemon"`, delete `~/.local/share/hq`.
  Repos are never touched; engineer branches live in your repos' refs.
