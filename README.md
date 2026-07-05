# shipyard

**Talk to your projects like Slack channels. Ship with a crew of agents.**

shipyard is a Slack-like terminal UI for running fleets of coding agents.
Every project you work on is a **channel**. Each channel has a **lead agent**
you talk to; it delegates work to **crewmate** agents that run autonomously in
isolated git worktrees ([treehouse](https://github.com/kunchenguid/treehouse))
and deliver through the [no-mistakes](https://github.com/kunchenguid/no-mistakes)
validation pipeline. Tasks are **threads**. You have unread badges. It feels
like Slack, but everyone except you is an agent.

Successor to [firstmate](https://github.com/kunchenguid/firstmate) — same
ideas, real application: a Go daemon owns the agent lifecycle deterministically
instead of a 101KB prose spec, and agents speak structured stream-json instead
of being screen-scraped through tmux.

## Quick start

```sh
shipyard            # from anywhere — opens the TUI, auto-starts the daemon
```

No cd-ing into a repo. In `#home`, tell the assistant:

> new channel beta-os with repos ~/code/beta-os-api and ~/code/beta-os-web

Then open `#beta-os` and talk to the lead. It spawns crewmates; each task is a
thread; press `t` on a thread to drop into the crewmate's live session in
tmux.

## Docs

- **[docs/GUIDE.md](docs/GUIDE.md) — the user guide** (start here; also `?` in
  the TUI or `shipyard help`)
- [SPEC.md](SPEC.md) — product spec
- [ARCHITECTURE.md](ARCHITECTURE.md) — implementation design

## Status

Early — under active construction. See SPEC for v1 scope.
