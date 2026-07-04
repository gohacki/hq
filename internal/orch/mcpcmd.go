package orch

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gohacki/shipyard/internal/config"
	"github.com/gohacki/shipyard/internal/mcp"
	"github.com/gohacki/shipyard/internal/rpc"
)

// RunLeadMCP implements `shipyard mcp-lead --channel <id> [--home]`: the MCP
// stdio server Claude Code spawns for a lead agent. Every tool call turns
// into a daemon RPC over the unix socket — leads never touch state directly.
func RunLeadMCP(paths config.Paths, args []string) error {
	fs := flag.NewFlagSet("mcp-lead", flag.ContinueOnError)
	channelID := fs.String("channel", "", "channel id this lead belongs to")
	home := fs.Bool("home", false, "expose home-assistant tools")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *channelID == "" {
		return fmt.Errorf("--channel is required")
	}
	cl, err := rpc.Dial(paths.SocketPath())
	if err != nil {
		return fmt.Errorf("shipyard daemon not reachable: %w", err)
	}
	defer cl.Close()

	srv := &mcp.Server{Name: "shipyard", Version: "0.1.0", Tools: leadTools(cl, *channelID, *home)}
	return srv.Serve(os.Stdin, os.Stdout)
}

func obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func leadTools(cl *rpc.Client, channelID string, home bool) []mcp.Tool {
	call := func(method string, params any) (string, error) {
		var out json.RawMessage
		if err := cl.Call(method, params, &out); err != nil {
			return "", err
		}
		return string(out), nil
	}

	tools := []mcp.Tool{
		{
			Name:        "create_task",
			Description: "Delegate work to a new crewmate agent. kind=ship delivers a code change through the channel's delivery mode; kind=scout investigates and writes a report. The brief is all the crewmate sees besides channel instructions — make it rich: goal, context, constraints, definition of done.",
			InputSchema: obj(map[string]any{
				"repo":  str("repo name within this channel (omit if the channel has exactly one)"),
				"kind":  map[string]any{"type": "string", "enum": []string{"ship", "scout"}},
				"title": str("short imperative title"),
				"brief": str("full task brief (markdown)"),
			}, "kind", "title", "brief"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct{ Repo, Kind, Title, Brief string }
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("task.create", map[string]any{
					"channel_id": channelID, "repo": p.Repo, "kind": p.Kind, "title": p.Title, "brief": p.Brief,
				})
			},
		},
		{
			Name:        "list_tasks",
			Description: "List this channel's tasks with live status (queued|running|needs-input|blocked|delivering|attached|done|failed|abandoned).",
			InputSchema: obj(map[string]any{}),
			Run: func(json.RawMessage) (string, error) {
				return call("tasks.list", map[string]any{"channel_id": channelID})
			},
		},
		{
			Name:        "message_task",
			Description: "Send a steering message to a task's crewmate (appears in the task thread).",
			InputSchema: obj(map[string]any{
				"task_id": str("task id"),
				"body":    str("message to the crewmate"),
			}, "task_id", "body"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					TaskID string `json:"task_id"`
					Body   string `json:"body"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("task.message", map[string]any{"task_id": p.TaskID, "body": p.Body, "author": "lead"})
			},
		},
		{
			Name:        "read_report",
			Description: "Read the report produced by a scout task.",
			InputSchema: obj(map[string]any{"task_id": str("scout task id")}, "task_id"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct {
					TaskID string `json:"task_id"`
				}
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("report.read", map[string]any{"task_id": p.TaskID})
			},
		},
		{
			Name:        "read_instructions",
			Description: "Read this channel's instructions doc (injected into every crewmate brief).",
			InputSchema: obj(map[string]any{}),
			Run: func(json.RawMessage) (string, error) {
				return call("instructions.get", map[string]any{"channel_id": channelID})
			},
		},
		{
			Name:        "edit_instructions",
			Description: "Replace this channel's instructions doc. Use when the captain states durable conventions, goals, or constraints.",
			InputSchema: obj(map[string]any{"body": str("full new markdown body")}, "body"),
			Run: func(a json.RawMessage) (string, error) {
				var p struct{ Body string }
				if err := json.Unmarshal(a, &p); err != nil {
					return "", err
				}
				return call("instructions.set", map[string]any{"channel_id": channelID, "body": p.Body})
			},
		},
	}

	if home {
		tools = append(tools,
			mcp.Tool{
				Name:        "create_channel",
				Description: "Create a new project channel: name (lowercase-kebab), local repo paths it spans, delivery mode (no-mistakes | direct-pr | local-only; default no-mistakes).",
				InputSchema: obj(map[string]any{
					"name":     str("channel name, e.g. beta-os"),
					"repos":    map[string]any{"type": "array", "items": str("absolute or ~/ local repo path"), "description": "repos this channel spans"},
					"delivery": map[string]any{"type": "string", "enum": []string{"no-mistakes", "direct-pr", "local-only"}},
				}, "name", "repos"),
				Run: func(a json.RawMessage) (string, error) {
					var p struct {
						Name     string   `json:"name"`
						Repos    []string `json:"repos"`
						Delivery string   `json:"delivery"`
					}
					if err := json.Unmarshal(a, &p); err != nil {
						return "", err
					}
					return call("channels.create", p)
				},
			},
			mcp.Tool{
				Name:        "list_channels",
				Description: "List all channels with their repos and delivery modes.",
				InputSchema: obj(map[string]any{}),
				Run: func(json.RawMessage) (string, error) {
					return call("channels.list", nil)
				},
			},
		)
	}
	return tools
}
