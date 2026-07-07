# hq — product spec

An engineering department as a program. The boss (the human) stays deeply
involved in **planning** and **verification**; the department handles
everything else invisibly. Deterministic Go owns the agent lifecycle; agents
own only judgment.

## Why this shape

v1 (shipyard) was a Slack-like chat UI. Chat optimizes for conversation, but
running a department is **decision routing**: 90% of agent activity needs no
attention, and the 10% that does should queue with full context and one-key
actions — not hide in channel scrollback. v2 rebuilds the UX around that.

## The org

- **You: the boss.** Your surface is the **kanban board** (home) plus a
  **⚠ needs-you inbox** pinned at the top of the sidebar.
- **Director (one, global)** — the **◆ hq** home chat (no PM role; the director creates projects and answers cross-project questions).
  Creates projects conversationally, routes asks, knows every project's
  state (cross-project tools). Never writes code.
- **EM (one per project)** — plans and delegates. For non-trivial asks it
  MUST propose a plan (doc + proposed tickets + open questions) that you
  review before work starts. On-demand with durable session memory; never
  writes code (prompt-enforced role; full tool access for reading context
  like your ticket tracker via your global MCP servers).
- **Engineers (one per ticket)** — autonomous Claude Code sessions. Every
  ticket owns a native git worktree of EVERY project repo (same branch,
  env files pre-copied per the playbook, decomposed together on landing —
  branches stay in the user's repos). Ticket kinds: **build** (deliver a
  change through the project's delivery mode) and **spike** (investigate →
  report with proposed follow-up tickets).
- **The playbook (per project)** — the captured SDLC: a prose lifecycle
  doc plus a machine recipe (env globs, install/verify, dev servers +
  ports, CI pipeline mirror), written during the EM's one-time setup
  interview (which includes a no-mistakes gate walkthrough) and read with
  `p`. Ticket intake grills the boss (one question at a time, recommended
  answers, skippable) before planning.

## The inbox (⚠ needs you)

Attention items, interrupts first, pinned at the top of the sidebar and
mirrored as a banner inside the chat they belong to, with one-key actions
(`enter` open · `a` approve · `r` retry · `d` dismiss · `1-9` options):

| kind | tier | source |
|---|---|---|
| plan | interrupt | EM proposed a plan — review screen |
| question / options | interrupt | agent blocked on a decision (options render as 1-9 cards) |
| blocked / failed | interrupt | ticket stuck or crashed (retry re-opens it fresh) |
| demo | break | build ticket ready for manual verification |
| handbook | break | proposed handbook edit awaiting approval |

Items auto-resolve when their ticket moves on. **Presence** (`M`):
heads-down (notify interrupts only) · available (+demos/handbook) · review
(everything). The queue itself always holds everything.

## Planning

The EM's `propose_plan` produces a plan artifact: markdown doc, proposed
tickets, open questions. The **plan review screen** shows the doc; you
toggle tickets in/out (`space`), answer questions inline (`enter`), then
`A` approve — accepted tickets spawn, and the EM receives your answers and
edits — or `R` request changes. `ask_boss` gives agents option-card
questions for one-keypress decisions.

## Verification (demos)

Project `verify` setting: `on-completion` (default) | `before-delivery` |
`none`. At that stage a build engineer must post a **demo**: what changed +
copy-paste test steps + a dev server **running from its own worktree on a
unique port**, ending with a `DEMO:` marker → a demo card in your office.
The ticket parks (worktree and server stay alive) until you approve —
`a` sends the engineer confirmation to finish; only then `STATUS: done` and
teardown.

## Tickets, delivery, teardown

- Delivery per project: `no-mistakes` (default; full pipeline → PR → CI,
  ask-user gate findings become office questions) | `direct-pr` |
  `local-only`.
- Deterministic supervision: engineer turn-ends must end with
  `DEMO:` / `STATUS: done` / `STATUS: blocked` / `QUESTION:` — classified
  without a model in the loop.
- Worktrees auto-return to the treehouse pool when tickets land —
  fail-closed: kept if uncommitted changes or orphan commits remain.
- Failed tickets retry fresh (`r`) with the same brief.

## Knowledge

- **Team handbook** per project (conventions, goals), injected into every
  engineer brief; edited via `e`, or through EM **handbook edit proposals**
  you approve with one key (the department learns from your corrections).
- **Onboarding docs**: project creation auto-spikes each repo to document
  local development — including running multiple simultaneous dev servers
  from different worktrees (port/db/cache overrides) — into the handbook.
  Cached per repo across projects; `refresh_onboarding` re-maps on change;
  engineers self-heal it when it proves wrong.

## Views

- **Board** (home): kanban — backlog · in progress · verify · needs-you ·
  done, scoped to one project (sidebar enter) or all. Verify cards carry
  the demo's dev-server URL. **Chats** (ticket/project), **plan review**,
  `?` help overlay. Vim-native throughout (h/l/j/k, gg/G, `/` search,
  `:` command line).
- **Chat is the primary surface**: an hq-rendered thread — you, the EM, the
  engineer — with system events interleaved as one-line "· event" markers
  and a metadata header (status · model · worktree · dev URL · age).
  Sessions stay headless; the composer is always live.
- **Attach (`v`) is the escape hatch**: a picker lists the project's EM and
  live engineers; picking one checks that session out of supervision and
  opens the real interactive Claude Code CLI in a NEW tmux window (info
  header on top). Closing the window / exiting the CLI checks it back in —
  same session id, nothing lost. Requires hq to run inside tmux.

## Models

Whole department defaults to **sonnet**. `/model` in composers
(project → EM, thread → engineer, `/model eng <m>` project default), or ask
the EM ("upgrade yourself to opus"); `create_ticket` takes a per-ticket
model. Switches kill + resume the same session with a new `--model` — no
context lost. `fable` → `claude-fable-5`.

## Startup

One global binary: `hq` from anywhere auto-starts the daemon; closing the
TUI stops nothing. State in `~/.local/share/hq` (`HQ_DATA_DIR` override);
`SHIPYARD`-era data is not migrated (v2 is a fresh start).
