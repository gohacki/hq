# hq

**An engineering department as a program.**

hq runs a department of AI agents for you: a **PM** that takes intake in the
Conference Room, an **EM** per project that plans and delegates, and
**engineers** that work tickets autonomously in isolated git worktrees
([treehouse](https://github.com/kunchenguid/treehouse)), delivering through
the [no-mistakes](https://github.com/kunchenguid/no-mistakes) pipeline.

You stay in the loop for exactly two things — **planning** and
**verification** — and out of the loop for everything else. The home screen
is **My Office**: a decision queue holding only what needs you. Plans arrive
for review before work starts; finished work arrives as a **demo** with a
running dev server and copy-paste test steps. Everything in between is
invisible unless you go looking (the board, ticket timelines, desk visits).

## Quick start

```sh
hq            # from anywhere, inside tmux — opens the TUI, auto-starts the daemon
```

In the Conference Room, tell the PM:

> new project beta-os with repos ~/code/beta-os-api and ~/code/beta-os-web

Then open `#beta-os`, brief the EM, and work your office queue. Press `?`
for help anywhere.

## Docs

- **[docs/GUIDE.md](docs/GUIDE.md) — the user guide** (start here; also `?`
  in the TUI or `hq help`)
- [SPEC.md](SPEC.md) — product spec
- [ARCHITECTURE.md](ARCHITECTURE.md) — implementation design

## History

hq v2 is the company-themed rebuild of shipyard (a Slack-like v1), itself a
successor to [firstmate](https://github.com/kunchenguid/firstmate).
