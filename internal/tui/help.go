package tui

// openHelp shows the help overlay in the main viewport.
func (m *model) openHelp() {
	m.showHelp = true
	m.vp.SetContent(helpBody)
	m.vp.GotoTop()
}

const helpBody = `  HQ HELP                                      (any key closes; j/k scroll)

  THE COMPANY
    hq runs an engineering department for you (the boss). The PM in the
    Conference Room creates projects; each project has an EM you brief and
    engineers who work tickets (build = ship a change, spike = investigate)
    in isolated git worktrees. Nothing needs your attention unless it's in
    MY OFFICE — the home screen decision queue.

  MY OFFICE (home screen)
    Cards for everything awaiting you: plan reviews, demos, questions,
    blockers, handbook edits. Interrupts sort above when-you-have-a-minute.
    j/k     move between cards
    enter   open: plans → review screen · options → pick 1-9 · else thread
    a       approve (demo → tell engineer to finish; handbook → apply)
    o       open the thread behind the card
    v       visit the engineer's desk (tmux)
    r       retry a failed ticket fresh
    x       dismiss/resolve the card

  PLAN REVIEW
    The EM proposes plans for non-trivial work: a doc, proposed tickets,
    open questions. space toggles a ticket in/out, enter answers a
    question, A approves (spawns checked tickets), R requests changes.

  PROJECTS & CHAT
    tab to the sidebar: ◉ my office · ▤ board · ◇ conference room (PM) ·
    # projects (ticket threads nest under the open one; staff below).
    In a ticket thread you see a TIMELINE (agent chatter collapsed to first
    lines); x expands the full chat. enter composes; anything you type goes
    to that agent. 1-9 promotes a spike report's proposed tickets.
    e edits the team handbook in $EDITOR.

  BOARD (b)
    Department overview: queued · building · delivering · needs you · done.
    h/l/j/k move, enter opens the ticket.

  DESK VISITS (v or t)
    A tmux window with the agent's live Claude session (left) and a console
    in their worktree (right). Close the window to hand control back.

  PRESENCE (M)
    🟢 available: notified of interrupts + demos/handbook edits
    🎧 heads-down: interrupts only
    👀 review: everything
    The office queue always holds all of it regardless of mode.

  MODELS
    Everyone runs sonnet by default. /model in a composer:
      /model opus          project chat → the EM · ticket thread → engineer
      /model eng haiku     project default for future engineers
      /model em fable      explicit EM switch
    Or just tell the EM ("upgrade yourself to opus"). Switches resume the
    same session — nothing is lost.

  DEMOS
    With verify on (project setting), engineers stop and hand you a demo:
    what changed + copy-paste test steps + a dev server running from their
    own worktree. Approve with a; the worktree stays alive until you do.

  CLI
    hq help · hq doctor · hq call <method> [json] · docs/GUIDE.md`
