package tui

// openHelp shows the help overlay in the main viewport.
func (m *model) openHelp() {
	m.showHelp = true
	m.vp.SetContent(helpBody)
	m.vp.GotoTop()
}

const helpBody = `  HQ HELP                                      (any key closes; j/k scroll)

  THE COMPANY
    hq runs an engineering department for you (the boss). ◆ hq in the
    sidebar is the director: it creates projects and answers cross-project
    questions. Each project has its own EM you brief; engineers work
    tickets (build = ship a change, spike = investigate). Anything that
    needs you lands in ⚠ NEEDS YOU at the top of the sidebar.

  THE TICKET MACHINE
    A new project starts with a one-time SETUP INTERVIEW in its chat: the
    EM captures the whole lifecycle — env files, install/verify commands,
    dev servers + ports, the CI pipeline mirror, the no-mistakes delivery
    gate — into the project PLAYBOOK (press p to read it anywhere the
    project is in scope). After that, tickets are the whole interface:
    the EM grills you on each non-trivial ask (one question at a time,
    with recommendations; say "no grilling" to skip), proposes a plan, and
    every approved ticket gets an isolated git worktree of EVERY project
    repo (same branch, env files pre-copied, decomposed together when the
    ticket lands — branches stay in your repos).

  THE BOARD (home)
    Kanban of tickets: backlog · in progress · verify · needs you · done.
    Scoped to one project (b from its chat) or all projects. Enter on a
    sidebar project row opens its EM chat; b flips to that board.
    Verify cards carry the engineer's dev-server URL for manual checks.
    h/l/j/k move (h past the first column reaches the sidebar), 0/$ jump
    to the first/last column, gg/G to a column's top/bottom, enter opens
    the ticket's chat.

  CHAT (a ticket or project)
    The thread is the surface: you, the EM, and the engineer, with dim
    "· event" lines (started, demo posted, done, worktree returned) woven
    in. i or enter composes; esc leaves. A ticket chat's header shows
    status · model · worktree · dev URL · age.
    Scrolling: l from the sidebar enters scroll mode — j/k, gg/G, and
    ctrl+d/u/f/b move the thread (ctrl+d/u/f/b also work straight from the
    sidebar). New messages only auto-follow when you're at the bottom.
    When a decision is waiting, a banner appears above the composer:
    a       approve (demo → engineer finishes; handbook → applied)
    r       retry a failed/blocked ticket fresh
    d       dismiss (failed/blocked tickets are abandoned)
    1-9     answer an options question (or promote a spike proposal)
    P       open a proposed plan's review screen
    x       expand/collapse engineer chatter

  PLAYBOOK (p)
    Read the focused project's playbook — the agreed lifecycle doc plus
    the machine recipe (env globs, install/verify, dev servers, pipeline).
    Works from the sidebar, board, or chat. The EM edits it with its
    set_playbook tool; ask it to update the playbook when reality changes.

  LIVE SESSIONS (v)
    v opens the attach picker: choose the director/EM or a live engineer,
    and the agent's real CLI (Claude Code for engineers, pi for managers)
    opens in a NEW tmux window with an info header. Close the window or
    exit the CLI to hand the session back to headless supervision.
    Requires hq to be running inside tmux.

  PLAN REVIEW
    EMs propose plans for non-trivial work: a doc, proposed tickets, open
    questions. space toggles a ticket in/out, enter answers a question,
    A approves (spawns checked tickets), R requests changes.

  VIM
    h/l     move focus sidebar ↔ main (boards use h/l for columns first)
    esc     back out — always lands on the sidebar cursor (composer →
            sidebar; cards → sidebar; sidebar → unscope to all projects)
    gg / G  top / bottom of the focused list (or the chat timeline)
    ctrl+f/b ctrl+d/u   page / half-page scroll
    i       insert mode — focus the composer in a chat (esc leaves)
    /text   search the focused list (sidebar, plan, board);
            enter jumps to the first match, n/N repeat forward/back
    :cmd    command line — :q quit · :help · :board · :hq ·
            :model [em|eng] <m> · :presence [mode] · :handbook

  PRESENCE (M)
    🟢 available: notified of interrupts + demos/handbook edits
    🎧 heads-down: interrupts only
    👀 review: everything
    The inbox always holds all of it regardless of mode.

  MODELS
    Everyone runs sonnet by default. /model in a composer (or :model):
      /model opus          project chat → the EM · ticket thread → engineer
      /model eng haiku     project default for future engineers
      /model em fable      explicit EM switch
    Or just tell the EM ("upgrade yourself to opus"). Switches resume the
    same session — nothing is lost.

  DEMOS
    With verify on (project setting), engineers stop and hand you a demo:
    what changed + copy-paste test steps + a dev server running from their
    own worktree on a unique port. The ticket parks in the board's verify
    column (URL on the card) until you approve with a.

  PROJECTS
    D on a project row (sidebar): press again to permanently delete it —
    tickets, history, and worktree leases. Kept if any worktree has
    unlanded work (use hq call project.delete '{"project_id":"…",
    "force":true}' to discard it anyway). Never touches the repo on disk.

  CLI
    hq help · hq doctor · hq call <method> [json] · docs/GUIDE.md`
