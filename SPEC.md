# shipyard — product spec

A Slack-like TUI for commanding fleets of coding agents. Successor to
[firstmate](https://github.com/kunchenguid/firstmate), rebuilt as a real
application: deterministic Go code owns the agent lifecycle, agents own only
judgment.

## Why (vs firstmate)

firstmate proved the model — one orchestrator agent, autonomous crewmates in
isolated worktrees, zero-token supervision — but its implementation limits it:

- All orchestration logic lives in a 101KB prose `AGENTS.md` an LLM must
  faithfully follow. Fragile, untestable, non-deterministic.
- Agent I/O is `tmux send-keys` + screen-scraping. Brittle per-harness glue.
- No real UI: tmux panes plus a plain-text status table.
- Starting a session means cd-ing into the firstmate checkout and launching a
  harness there.

shipyard keeps the good ideas (worktree isolation via treehouse, no-mistakes
delivery gate, event-driven supervision, ship/scout task shapes) and replaces
the substrate.

## Core concepts

### Channel = project

A channel is a named project spanning **one or more repos** (e.g. `#beta-os`
owning `beta-os-api` + `beta-os-web`). Created via the **#home channel
wizard**: tell the home assistant "new channel beta-os with repos X, Y" and it
clones/registers the repos, initializes a treehouse pool per repo, picks a
delivery mode, and seeds the instructions doc conversationally. A quick
keybinding-driven form exists for non-conversational setup.

Channel settings (editable later):
- `repos`: list of registered repos (local path; cloned if given a URL)
- `delivery`: `no-mistakes` (default) | `direct-pr` | `local-only`
- `verify`: `on-completion` (default) | `before-delivery` | `none` — the
  stage at which crewmates hand work back to the captain for **manual
  verification**: a message with what changed + exact hand-test instructions
  (dev server started from the crewmate's own worktree on a unique port, URL
  included), ending in a question. `STATUS: done` — and worktree teardown —
  only after the captain signs off.
- `instructions`: path to the channel instructions doc

Channel creation also auto-spawns a **dev-runbook scout** per repo: it
investigates how local development works (deps, tests, dev server command)
and appends a `## Local development — <repo>` section to the channel
instructions, specifically documenting how to run multiple simultaneous dev
servers from different worktrees (port/db/cache overrides). Every future
brief inherits it.

### Lead agent (one per channel)

The channel's main voice. **On-demand with durable memory**: it spins up when
you message the channel or when a crewmate event needs judgment, resuming its
prior Claude session (`--resume`) so it feels continuous. Idle channels cost
zero tokens. The daemon's deterministic watcher absorbs routine crewmate
events without waking the lead.

The lead does **judgment only**: decomposing asks into tasks, writing crewmate
briefs, reviewing results, answering you. It never edits project code itself.
It acts through MCP tools the daemon exposes (`create_task`, `message_task`,
`read_report`, `edit_instructions`, …) — never shell scripts, never tmux.

### Threads = tasks

Main channel scroll is you ↔ lead. Every delegated task becomes a **thread**;
the crewmate's progress and messages stream into that thread. You can reply
inside a thread to steer the crewmate directly. Task shapes:

- **ship** — deliver a code change through the channel's delivery mode.
- **scout** — investigate/plan/audit; produces a **report** rendered as a rich
  message in the thread (keybinding opens it in `$EDITOR`/pager). Reports end
  with a structured `proposed tasks` section; one keypress converts a proposal
  into a new ship-task thread with the report attached to the brief
  (**scout→ship handoff**).

### Crewmates

Autonomous workers. Each runs **headless Claude Code**
(`claude -p --output-format stream-json --input-format stream-json`) inside a
**treehouse-leased worktree** of the relevant repo. Harness support is
Claude-first behind a small adapter interface (codex etc. later).

### Escape hatch (tmux)

A keybinding on any task thread opens a **new tmux window with two panes**:
left, the crewmate's session resumed interactively (`claude --resume
<session-id>`) in its worktree; right, an empty shell in the same worktree.
Headless supervision pauses while you're attached and resumes on the same
session afterwards.

### Delivery: no-mistakes by default

Ship crewmates in `no-mistakes` channels drive the `no-mistakes axi` pipeline
(intent → rebase → review → test → document → lint → push → PR → CI).
Gate findings with `ask-user` actions surface as messages in the task thread
for you to answer; the answer is relayed back as `axi respond`.

### Channel instructions

Each channel has an instructions doc (conventions, goals, constraints) stored
in the channel's data dir and injected into the lead's and every crewmate's
context. Edit it by telling the lead ("add: always run migrations locally
first") or by opening it in `$EDITOR` from the TUI.

## Startup

- One global binary: `shipyard` (installed on PATH). Run from **anywhere** —
  no cd-ing into a repo.
- `shipyard` opens the TUI; it auto-starts the **daemon** if not running.
- The daemon owns all state and agent processes; closing the TUI stops
  nothing. Reattach anytime; crewmates keep working overnight.
- Works inside tmux (required for the escape hatch; the TUI itself runs in
  any terminal).

## UI (Slack-like)

- Left sidebar: `#home` + channels, each with **unread badges**; task threads
  with activity indicators nest under their channel.
- Main pane: channel scroll or thread view; streaming agent messages.
- Composer at the bottom; `@` to address a specific crewmate in a thread.
- Notifications: terminal bell + macOS desktop notification when a thread
  needs your input (gate finding, blocked crewmate, question).
- Keybindings: vim-ish navigation, `e` open instructions/report in editor,
  `t` escape-hatch to tmux, `enter` open thread, `esc` back.

## V1 scope

Core loop (channels, lead chat, task threads, crewmate spawning into
treehouse worktrees, deterministic supervision, no-mistakes delivery),
tmux escape hatch, unread badges + notifications, scout tasks with rich
reports and scout→ship handoff.

Fast-follow (explicitly out of v1): recurring scout bots, channel knowledge
base (pinned reports injected into briefs), additional harness adapters,
channel-spanning search.
