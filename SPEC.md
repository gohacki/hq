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

- **You: the boss.** Your surface is **My Office** — a decision queue.
- **PM (one, global)** — lives in the **Conference Room**. Intake: creates
  projects conversationally, routes asks, knows every project's state
  (cross-project tools). Never writes code.
- **EM (one per project)** — plans and delegates. For non-trivial asks it
  MUST propose a plan (doc + proposed tickets + open questions) that you
  review before work starts. On-demand with durable session memory; never
  writes code (prompt-enforced role; full tool access for reading context
  like your ticket tracker via your global MCP servers).
- **Engineers (one per ticket)** — autonomous Claude Code sessions in
  treehouse-leased worktrees. Ticket kinds: **build** (deliver a change
  through the project's delivery mode) and **spike** (investigate → report
  with proposed follow-up tickets).

## My Office (home screen)

Attention items, interrupts first, each a card with context and one-key
actions (`enter` open · `a` approve · `o` thread · `v` desk visit ·
`r` retry · `x` dismiss):

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

- **My Office** (default), **Board** (`b`: queued · building · delivering ·
  needs-you · done, cards aging), **Conference Room / project chats**
  (ticket threads render as **timelines** — agent chatter collapsed to
  first lines, `x` expands), **plan review**, `?` help overlay.
- **Desk visits** (`v`): tmux window, agent's live session left (same
  model, EMs get their MCP tools), console in their worktree right;
  closing hands control back to headless supervision.

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
