package orch

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gohacki/hq/internal/config"
	"github.com/gohacki/hq/internal/mcp"
	"github.com/gohacki/hq/internal/rpc"
)

// RunEMMCP implements `hq mcp-em --project <id> [--pm]`: the MCP stdio
// server Claude Code spawns for an EM (or the PM). Every tool call turns
// into a daemon RPC over the unix socket — managers never touch state
// directly.
func RunEMMCP(paths config.Paths, args []string) error {
	fs := flag.NewFlagSet("mcp-em", flag.ContinueOnError)
	projectID := fs.String("project", "", "project id this manager belongs to")
	pm := fs.Bool("pm", false, "expose PM (conference room) tools")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return fmt.Errorf("--project is required")
	}
	cl, err := rpc.Dial(paths.SocketPath())
	if err != nil {
		return fmt.Errorf("hq daemon not reachable: %w", err)
	}
	defer cl.Close()

	srv := &mcp.Server{Name: "hq", Version: "2.0.0", Tools: emTools(cl, *projectID, *pm)}
	return srv.Serve(os.Stdin, os.Stdout)
}

func obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func emTools(cl *rpc.Client, projectID string, pm bool) []mcp.Tool {
	call := func(method string, params any) (string, error) {
		var out json.RawMessage
		if err := cl.Call(method, params, &out); err != nil {
			return "", err
		}
		return string(out), nil
	}

	ticketSchema := obj(map[string]any{
		"repo":  str("repo name within this project (omit if the project has exactly one)"),
		"kind":  map[string]any{"type": "string", "enum": []string{"build", "spike"}},
		"title": str("short imperative title"),
		"brief": str("full ticket brief (markdown)"),
		"model": map[string]any{"type": "string", "enum": []string{"sonnet", "opus", "fable", "haiku"}, "description": "model for this engineer (default: project engineer model, normally sonnet). Upgrade for genuinely hard tickets."},
	}, "kind", "title", "brief")

	tools := []mcp.Tool{
		{
			Name:        "propose_plan",
			Description: "Propose a plan for a non-trivial ask: a short plan doc, the proposed tickets, and open questions. It lands in the boss's office for review — accepted tickets spawn automatically and you receive the boss's decisions/answers as a message. Use this INSTEAD of create_ticket for anything with scope decisions.",
			InputSchema: obj(map[string]any{
				"doc_md":    str("plan document (markdown): goal, approach, risks — concise"),
				"tickets":   map[string]any{"type": "array", "items": ticketSchema, "description": "proposed tickets in execution order"},
				"questions": map[string]any{"type": "array", "items": str("an open question for the boss"), "description": "open questions (optional)"},
			}, "doc_md", "tickets"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					DocMD     string           `json:"doc_md"`
					Tickets   []ProposedTicket `json:"tickets"`
					Questions []string         `json:"questions"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("plan.propose", map[string]any{
					"project_id": projectID, "doc_md": p.DocMD, "tickets": p.Tickets, "questions": p.Questions,
				})
			},
		},
		{
			Name:        "ask_boss",
			Description: "Ask the boss a blocking question. Provide 2-4 options with tradeoffs whenever the choice is enumerable — the boss picks with one keypress. The answer arrives as your next message.",
			InputSchema: obj(map[string]any{
				"question": str("the question, one sentence"),
				"options": map[string]any{"type": "array", "items": obj(map[string]any{
					"label":  str("short option label"),
					"detail": str("one-line tradeoff"),
				}, "label"), "description": "optional concrete options"},
				"ticket_id": str("attach to a ticket thread (optional)"),
			}, "question"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					Question string              `json:"question"`
					Options  []map[string]string `json:"options"`
					TicketID string              `json:"ticket_id"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("ask.boss", map[string]any{
					"project_id": projectID, "ticket_id": p.TicketID, "question": p.Question, "options": p.Options,
				})
			},
		},
		{
			Name:        "create_ticket",
			Description: "Open one ticket directly (trivial asks or boss-given lists only — use propose_plan for anything with scope decisions). kind=build delivers a code change through the project's delivery mode; kind=spike investigates and writes a report. The brief is all the engineer sees besides the team handbook — make it rich.",
			InputSchema: ticketSchema,
			Run: func(a json.RawMessage) (string, error) {
				var p struct{ Repo, Kind, Title, Brief, Model string }
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("ticket.create", map[string]any{
					"project_id": projectID, "repo": p.Repo, "kind": p.Kind, "title": p.Title, "brief": p.Brief, "model": p.Model,
				})
			},
		},
		{
			Name:        "create_tickets",
			Description: "Open MANY tickets at once (e.g. one per item of a boss-approved list). Same semantics as create_ticket. Returns per-ticket ids/errors.",
			InputSchema: obj(map[string]any{
				"tickets": map[string]any{"type": "array", "items": ticketSchema},
			}, "tickets"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					Tickets json.RawMessage `json:"tickets"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("tickets.create_batch", map[string]any{
					"project_id": projectID, "tickets": p.Tickets,
				})
			},
		},
		{
			Name:        "list_tickets",
			Description: "List this project's tickets with live status (queued|running|needs-input|blocked|delivering|visiting|done|failed|abandoned).",
			InputSchema: obj(map[string]any{}),
			Run: func(json.RawMessage) (string, error) {
				return call("tickets.list", map[string]any{"project_id": projectID})
			},
		},
		{
			Name:        "message_engineer",
			Description: "Send a steering message to a ticket's engineer (appears in the ticket thread).",
			InputSchema: obj(map[string]any{
				"ticket_id": str("ticket id"),
				"body":      str("message to the engineer"),
			}, "ticket_id", "body"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					TicketID string `json:"ticket_id"`
					Body     string `json:"body"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("ticket.message", map[string]any{"ticket_id": p.TicketID, "body": p.Body, "author": "em"})
			},
		},
		{
			Name:        "read_report",
			Description: "Read the report produced by a spike.",
			InputSchema: obj(map[string]any{"ticket_id": str("spike ticket id")}, "ticket_id"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					TicketID string `json:"ticket_id"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("report.read", map[string]any{"ticket_id": p.TicketID})
			},
		},
		{
			Name:        "read_handbook",
			Description: "Read this project's team handbook (injected into every engineer brief).",
			InputSchema: obj(map[string]any{}),
			Run: func(json.RawMessage) (string, error) {
				return call("handbook.get", map[string]any{"project_id": projectID})
			},
		},
		{
			Name:        "propose_handbook_edit",
			Description: "Propose a new full handbook body when the boss states durable conventions. Lands in the boss's office for one-key approval — never silently rewrite the handbook.",
			InputSchema: obj(map[string]any{
				"summary":  str("one-line summary of what changed"),
				"new_body": str("the FULL new handbook markdown body"),
			}, "summary", "new_body"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					Summary string `json:"summary"`
					NewBody string `json:"new_body"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("handbook.propose", map[string]any{
					"project_id": projectID, "summary": p.Summary, "new_body": p.NewBody,
				})
			},
		},
		{
			Name:        "refresh_onboarding",
			Description: "Re-map how local development works for a repo and replace the 'Local development' section of the team handbook (and the shared per-repo cache). Use when the boss says the dev setup/process changed.",
			InputSchema: obj(map[string]any{
				"repo": str("repo name within this project (omit if the project has exactly one)"),
			}),
			Run: func(a json.RawMessage) (string, error) {
				var p struct{ Repo string }
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("onboarding.refresh", map[string]any{"project_id": projectID, "repo": p.Repo})
			},
		},
		{
			Name:        "set_model",
			Description: "Change an agent's model on the fly (session resumes with full context). scope=self switches you (next turn), scope=ticket switches a live engineer, scope=eng sets this project's default for future engineers. Models: sonnet (default, cheap) | opus | fable (strongest) | haiku (fastest).",
			InputSchema: obj(map[string]any{
				"scope":     map[string]any{"type": "string", "enum": []string{"self", "ticket", "eng"}},
				"ticket_id": str("required when scope=ticket"),
				"model":     map[string]any{"type": "string", "enum": []string{"sonnet", "opus", "fable", "haiku"}},
			}, "scope", "model"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					Scope    string `json:"scope"`
					TicketID string `json:"ticket_id"`
					Model    string `json:"model"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				scope := p.Scope
				if scope == "self" {
					scope = "em"
				}
				return call("model.set", map[string]any{
					"project_id": projectID, "ticket_id": p.TicketID, "scope": scope, "model": p.Model,
				})
			},
		},
	}

	if pm {
		tools = append(tools,
			mcp.Tool{
				Name:        "create_project",
				Description: "Create a new project: name (lowercase-kebab), local repo paths it spans, delivery mode (no-mistakes | direct-pr | local-only; default no-mistakes), verify mode (none | before-delivery | on-completion; default on-completion — when engineers hand work back as a demo).",
				InputSchema: obj(map[string]any{
					"name":     str("project name, e.g. beta-os"),
					"repos":    map[string]any{"type": "array", "items": str("absolute or ~/ local repo path"), "description": "repos this project spans"},
					"delivery": map[string]any{"type": "string", "enum": []string{"no-mistakes", "direct-pr", "local-only"}},
					"verify":   map[string]any{"type": "string", "enum": []string{"none", "before-delivery", "on-completion"}},
				}, "name", "repos"),
				Run: func(a json.RawMessage) (string, error) {
					var p struct {
						Name     string   `json:"name"`
						Repos    []string `json:"repos"`
						Delivery string   `json:"delivery"`
						Verify   string   `json:"verify"`
					}
					if err := json.Unmarshal(a, &p); err != nil {
						return "", err
					}
					return call("projects.create", p)
				},
			},
			mcp.Tool{
				Name:        "list_projects",
				Description: "List all projects with their repos, delivery and verify modes.",
				InputSchema: obj(map[string]any{}),
				Run: func(json.RawMessage) (string, error) {
					return call("projects.list", nil)
				},
			},
			mcp.Tool{
				Name:        "list_all_tickets",
				Description: "List every ticket across all projects with live status (department overview).",
				InputSchema: obj(map[string]any{}),
				Run: func(json.RawMessage) (string, error) {
					return call("tickets.list", map[string]any{"project_id": ""})
				},
			},
		)
	}
	return tools
}
