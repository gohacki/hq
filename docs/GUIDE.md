# shipyard user guide

Everything you need to run a fleet. For the product rationale see
[SPEC.md](../SPEC.md); for internals see [ARCHITECTURE.md](../ARCHITECTURE.md).

## The mental model

shipyard looks like Slack, but everyone except you is an agent:

| Slack thing | shipyard thing |
|---|---|
| Workspace | your machine (one daemon, one TUI) |
| Channel | a **project** — one or more git repos |
| The person you DM in a channel | the channel's **lead agent** (delegates, never codes) |
| Thread | a **task** — one crewmate working in an isolated git worktree |
| Thread participants | the **crewmate** (autonomous coding agent) + you |
| `#general` | `#home` — the assistant that creates channels |
| Unread badge | messages you haven't seen; red = someone needs you |

You are "the captain". You talk; leads decompose and delegate; crewmates do
the work in [treehouse](https://github.com/kunchenguid/treehouse) worktrees
and deliver through the channel's delivery mode.

## Install & start

```sh
go build -o ~/.local/bin/shipyard ./cmd/shipyard   # from the repo
shipyard doctor    # checks claude, tmux, treehouse, no-mistakes, git
shipyard           # opens the TUI from anywhere; auto-starts the daemon
```

Run inside **tmux** if you want the escape hatch (`t`). Quitting the TUI
stops nothing — the daemon and every crewmate keep working; run `shipyard`
again to reattach.

## Creating a channel

Open `#home` and say what you want:

> new channel beta-os with repos ~/code/beta-os-api and ~/code/beta-os-web,
> delivery no-mistakes, verify before-delivery

The assistant registers the repos (each gets a treehouse pool), seeds the
channel instructions doc, and auto-spawns a **dev-runbook scout** per repo
that documents how local development works — including how to run several
dev servers from different worktrees at once — into the channel
instructions. Every future crewmate inherits that knowledge.

Settings you pick at creation (all editable later by asking the lead):

- **delivery** — how ship tasks land:
  - `no-mistakes` (default): full validation pipeline → push → PR → CI.
  - `direct-pr`: branch, push, draft PR. No gate.
  - `local-only`: branch + commit only; you merge by hand.
- **verify** — when crewmates hand work back for your manual check:
  - `on-completion` (default): after delivery, before the task closes.
  - `before-delivery`: after implementing, before any push/PR.
  - `none`: no handback.

## Working in a channel

Type in the composer to talk to the **lead**. Ask for anything: "fix the
login redirect", "what's in flight?", "investigate why CI is slow". The lead
creates tasks; each becomes a **thread** nested under the channel in the
sidebar.

Task kinds:
- **ship** — delivers a code change through the delivery mode.
- **scout** — investigates and posts a **report** into the thread. Reports
  end with proposed follow-up tasks: press `1`–`9` in the thread to promote
  one into a ship task (the report rides along as context).

**Bulk intake:** paste a whole ticket list ("here are 8 Linear tickets: … —
create a task for each") and the lead fans them out in one shot
(`create_tasks`). To let leads *pull* tickets themselves, allow your ticket
tool in `~/.config/shipyard/config.json` (see Configuration below).

Open a thread (`enter` on it) to watch the crewmate work or steer it
directly — anything you type there goes to that crewmate. Statuses in the
sidebar: `●` running · `✋` needs you · `🚀` delivering · `⌨` attached ·
`✓` done · `✗` failed.

### Verification handback

With verify on, a ship crewmate will stop at the configured stage and post:
what changed, exact copy-paste test instructions, and — if the project has a
dev server — a **running server started from its own worktree on a unique
port**, URL included. The task parks (red badge + notification) until you
answer in the thread. The worktree (and your test server) stays alive until
you approve; only then does it report done and release the worktree.

### Questions and gates

Whenever a crewmate hits something only you can decide — a question, a
blocker, or an `ask-user` finding from the no-mistakes gate — the thread
goes `✋ needs-input`, the channel badge lights up, and you get a desktop
notification. Reply in the thread to unblock it.

## Models

Everything runs on **sonnet** by default (cheap, fast). Upgrade or
downgrade any agent on the fly — switches resume the same session, so no
context is lost:

```
/model                 show current models for what you're looking at
/model opus            channel view → upgrade the lead; thread → that crewmate
/model crew haiku      default for this channel's future crewmates
/model lead fable      explicit lead switch from anywhere in the channel
```

Or just tell the lead: *"upgrade yourself to opus"*, *"run the next task on
fable"* — it has the same controls. Models: `sonnet` · `opus` · `fable` ·
`haiku` (full `claude-*` ids also accepted).

## Keybindings

The composer has focus by default; `tab` switches between composer and
sidebar.

The sidebar lists channels (threads nested under the open one), then a
**crew** section: the open channel's lead and every live crewmate. `enter`
on a crew member drops you into their live Claude session in a tmux window
(console pane alongside) — same as the `t` escape hatch, but agent-centric.

| Key | Where | Does |
|---|---|---|
| `tab` | anywhere | toggle composer ↔ sidebar focus |
| `enter` | composer | send message |
| `shift+enter` | composer | newline |
| `enter` | sidebar | open channel / thread, or a crew member's live session |
| `j` / `k` (or arrows) | sidebar | move selection |
| `esc` | anywhere | thread → channel; composer → sidebar |
| `e` | sidebar focus | edit channel instructions in `$EDITOR` |
| `t` | sidebar focus | escape hatch: tmux window with the crewmate live |
| `1`–`9` | thread, sidebar focus | promote scout proposal N to a ship task |
| `g` / `G` | sidebar focus | scroll to top / bottom |
| `ctrl+d` / `ctrl+u` | sidebar focus | scroll half page |
| `?` | sidebar focus | help overlay (also `/help` in the composer) |
| `q` / `ctrl+c` | sidebar focus / anywhere | quit TUI (fleet keeps running) |

Composer commands: `/model …` (above), `/help`.

## The escape hatch

`t` on a task thread opens a new tmux window: **left**, the crewmate's
session resumed in the full interactive Claude Code UI — its entire history
and context, yours to drive; **right**, a shell in its worktree. While
attached, headless supervision pauses (status `⌨`). Close the window and the
task parks; your next message in the thread resumes the same session
headlessly.

## Channel instructions

Each channel has an instructions doc injected into the lead and every
crewmate brief: conventions, goals, constraints, plus the auto-generated
"Local development" sections. Edit it by telling the lead ("add: always run
migrations locally first") or press `e` to open it in your editor.

Runbooks stay current three ways: they're **cached per repo** (a repo
scouted by one channel is copied, not re-scouted, by the next), the lead has
a **`refresh_runbook`** tool ("the dev setup changed — refresh the runbook"
re-scouts and replaces the section), and every crewmate is instructed to
**fix the runbook in place** if it proves wrong mid-task.

## Configuration

Optional `~/.config/shipyard/config.json`:

```json
{
  "lead_allowed_tools": ["mcp__linear"]
}
```

- `lead_allowed_tools` — extra tool namespaces lead agents may call.
  Headless agents inherit your global Claude MCP servers automatically;
  crewmates can use all of them (they run unrestricted in their worktrees),
  but leads are locked to shipyard's own tools unless you widen this list —
  e.g. `mcp__linear` lets leads ingest tickets directly.

## CLI reference

```
shipyard              open the TUI (auto-starts the daemon)
shipyard daemon run   run the daemon in the foreground (debugging)
shipyard doctor       check required external tools
shipyard call <method> [json]   raw RPC to the daemon (scripting/debugging)
shipyard help         this, in short form
```

`shipyard call` examples:

```sh
shipyard call channels.list
shipyard call tasks.list '{"channel_id":"ch_…"}'
shipyard call model.set '{"channel_id":"ch_…","scope":"lead","model":"opus"}'
```

## Where things live

| Path | What |
|---|---|
| `~/.local/share/shipyard/shipyard.db` | all state (channels, tasks, messages) |
| `~/.local/share/shipyard/channels/<name>/` | instructions.md, per-task briefs & reports |
| `~/.local/share/shipyard/logs/daemon.log` | daemon log |
| `~/.local/share/shipyard/daemon.sock` | RPC socket |
| `~/.treehouse/…` | worktree pools (managed by treehouse) |

Env overrides: `SHIPYARD_DATA_DIR`, `SHIPYARD_CONFIG_DIR`, `SHIPYARD_MODEL`
(fleet default model).

## Troubleshooting

- **"daemon did not come up"** — check `~/.local/share/shipyard/logs/daemon.log`.
  A stale socket is replaced automatically; a second daemon refuses to start.
- **Escape hatch errors** — you need a running tmux server (start shipyard
  inside tmux).
- **Crewmate parked after a restart** — daemon restarts park in-flight tasks
  (`needs-input`, with a system message). Reply in the thread; the session
  resumes where it left off.
- **"worktree kept (uncommitted changes)"** — shipyard refuses to recycle a
  worktree holding unlanded work. Inspect it (path is in the message), then
  commit/discard and tell the lead, or return it yourself with
  `treehouse return --force <path>` from the repo.
- **Everything is stuck / start fresh** — `pkill -f "shipyard daemon"`, then
  (optionally) delete `~/.local/share/shipyard`. Repos are never touched;
  crewmate branches live in your repos' refs.
