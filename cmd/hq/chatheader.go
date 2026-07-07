package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/gohacki/hq/internal/config"
	"github.com/gohacki/hq/internal/daemon"
	"github.com/gohacki/hq/internal/rpc"
	"github.com/gohacki/hq/internal/store"
)

// runChatHeader is the tiny process that lives in the header pane hq splits
// above a live chat: it just polls the daemon and prints who you're talking
// to (agent/room, and the ticket's worktree/branch/status when there is
// one) — no interaction, no keys read, it exits when tmux kills its pane.
func runChatHeader(paths config.Paths, args []string) error {
	fs := flag.NewFlagSet("chat-header", flag.ContinueOnError)
	projectID := fs.String("project", "", "project id")
	ticketID := fs.String("ticket", "", "ticket id (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return fmt.Errorf("usage: hq chat-header --project <id> [--ticket <id>]")
	}

	cl, err := rpc.Dial(paths.SocketPath())
	if err != nil {
		return err
	}
	defer cl.Close()

	for {
		fmt.Print("\x1b[H\x1b[2J") // home + clear, before each redraw
		fmt.Println(renderChatHeader(cl, *projectID, *ticketID))
		time.Sleep(4 * time.Second)
	}
}

var (
	chHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	chDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	chAccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
)

func renderChatHeader(cl *rpc.Client, projectID, ticketID string) string {
	var projects []daemon.ProjectView
	if err := cl.Call("projects.list", nil, &projects); err != nil {
		return chDimStyle.Render("hq: " + err.Error())
	}
	var p daemon.ProjectView
	found := false
	for _, pr := range projects {
		if pr.ID == projectID {
			p, found = pr, true
			break
		}
	}
	if !found {
		return chDimStyle.Render("hq: project not found")
	}

	var lines []string
	switch {
	case p.Name == store.DirectorRoomName:
		lines = append(lines, chHeaderStyle.Render("◆ hq · director"))
	case ticketID == "":
		lines = append(lines, chHeaderStyle.Render("◉ EM · "+p.Name))
	default:
		lines = append(lines, chHeaderStyle.Render("⚙ Engineer · "+p.Name))
	}

	if p.Name != store.DirectorRoomName && ticketID == "" {
		repos := make([]string, len(p.Repos))
		for i, r := range p.Repos {
			repos[i] = r.Name
		}
		info := "delivery: " + orDash(p.Delivery) + " · verify: " + orDash(p.Verify)
		if len(repos) > 0 {
			info = "repos: " + strings.Join(repos, ", ") + "  ·  " + info
		}
		lines = append(lines, chDimStyle.Render(info))
	}

	if ticketID != "" {
		var tickets []daemon.TicketView
		if err := cl.Call("tickets.list", map[string]any{"project_id": projectID}, &tickets); err == nil {
			for _, t := range tickets {
				if t.ID != ticketID {
					continue
				}
				lines = append(lines, chAccentStyle.Render(fmt.Sprintf("▸ %s [%s]", t.Title, t.Kind)))
				status := "status: " + string(t.Status)
				if t.Model != "" {
					status += "  ·  model: " + t.Model
				}
				lines = append(lines, chDimStyle.Render(status))
				if t.Branch != "" || t.WorktreePath != "" {
					lines = append(lines, chDimStyle.Render("branch: "+orDash(t.Branch)+"  ·  worktree: "+orDash(t.WorktreePath)))
				}
				break
			}
		}
	}

	return strings.Join(lines, "\n")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
