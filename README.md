# hq

**An engineering department as a program.**

hq runs a department of AI agents for you: an **intake EM** in the ◆ hq
home chat that creates projects, an **EM** per project that plans and
delegates, and **engineers** that work tickets autonomously in isolated git
worktrees ([treehouse](https://github.com/kunchenguid/treehouse)),
delivering through the [no-mistakes](https://github.com/kunchenguid/no-mistakes)
pipeline.

You stay in the loop for exactly two things — **planning** and
**verification** — and out of the loop for everything else. The home screen
is a **kanban board** (backlog · in progress · verify · needs you · done)
with a **⚠ needs-you inbox** pinned in the sidebar. Opening a ticket is a
Slack-like chat with the EM and its engineer, events woven in; `v` attaches
the real Claude Code CLI in a new tmux window when you want hands on. Plans
arrive for review before work starts; finished work parks in **verify** as
a demo with a running dev server (URL on the card) and copy-paste test
steps. The whole TUI is vim-native (h/l/j/k, gg/G, `/` search, `:` cmds).

## Quick start

```sh
hq            # from anywhere, inside tmux — opens the TUI, auto-starts the daemon
```

In the ◆ hq chat, tell the intake EM:

> new project beta-os with repos ~/code/beta-os-api and ~/code/beta-os-web

Then open `#beta-os`, brief its EM, and watch the board. Press `?` for help
anywhere.

## Docs

- **[docs/GUIDE.md](docs/GUIDE.md) — the user guide** (start here; also `?`
  in the TUI or `hq help`)
- [SPEC.md](SPEC.md) — product spec
- [ARCHITECTURE.md](ARCHITECTURE.md) — implementation design

## History

hq v2 is the company-themed rebuild of shipyard (a Slack-like v1), itself a
successor to [firstmate](https://github.com/kunchenguid/firstmate).
