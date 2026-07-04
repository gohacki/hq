// Package orch is shipyard's orchestration layer: it runs lead agents and
// crewmates on top of the daemon core. The daemon stays deterministic; orch
// owns everything that touches an agent process.
package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gohacki/shipyard/internal/agent"
	"github.com/gohacki/shipyard/internal/daemon"
	"github.com/gohacki/shipyard/internal/store"
)

// leadIdleTimeout closes an idle lead process; its session resumes on the
// next message, so this only trades warm-start latency for memory.
const leadIdleTimeout = 15 * time.Minute

type Orch struct {
	d       *daemon.Daemon
	harness agent.Harness
	log     *slog.Logger

	mu    sync.Mutex
	leads map[string]*lead    // channel id → live lead process
	crews map[string]*crewRun // task id → live crewmate process
}

type lead struct {
	session agent.Session
	lastUse time.Time
}

func New(d *daemon.Daemon, h agent.Harness, log *slog.Logger) *Orch {
	o := &Orch{d: d, harness: h, log: log, leads: map[string]*lead{}, crews: map[string]*crewRun{}}
	o.registerHandlers()
	go o.reapIdleLeads()
	return o
}

// --- daemon.Orchestrator ---

func (o *Orch) CaptainChannelMessage(ctx context.Context, ch store.Channel, body string) {
	if err := o.sendToLead(ctx, ch, body); err != nil {
		o.log.Error("lead send failed", "channel", ch.Name, "err", err)
		o.systemMessage(ch.ID, "", "lead agent failed: "+err.Error())
	}
}

func (o *Orch) CaptainThreadMessage(ctx context.Context, t store.Task, body string) {
	if err := o.sendToCrew(ctx, t, body); err != nil {
		o.log.Error("crew send failed", "task", t.ID, "err", err)
		o.systemMessage(t.ChannelID, t.ID, "could not reach crewmate: "+err.Error())
	}
}

// Reconcile runs at daemon boot: live agent processes did not survive the
// restart, so park every previously-active task until the captain nudges it.
func (o *Orch) Reconcile(ctx context.Context, tasks []store.Task) {
	for _, t := range tasks {
		if t.Status == store.TaskQueued {
			continue // never started; spawn picks it up when created again
		}
		t.Status = store.TaskNeedsInput
		if err := o.d.UpdateTask(t); err != nil {
			o.log.Error("reconcile update failed", "task", t.ID, "err", err)
			continue
		}
		o.systemMessage(t.ChannelID, t.ID,
			"daemon restarted — crewmate session parked. Reply in this thread to resume it.")
	}
}

func (o *Orch) systemMessage(channelID, taskID, body string) {
	if _, err := o.d.PostMessage(store.Message{
		ChannelID: channelID, TaskID: taskID, Author: "system", Kind: "system", Body: body,
	}); err != nil {
		o.log.Error("system message failed", "err", err)
	}
}

// --- lead lifecycle ---

func (o *Orch) sendToLead(ctx context.Context, ch store.Channel, body string) error {
	o.mu.Lock()
	l, ok := o.leads[ch.ID]
	if ok {
		l.lastUse = time.Now()
	}
	o.mu.Unlock()
	if ok {
		if err := l.session.Send(body); err == nil {
			return nil
		}
		// Process died; fall through and restart with resume.
		o.dropLead(ch.ID)
	}
	return o.startLead(ctx, ch, body)
}

func (o *Orch) dropLead(channelID string) {
	o.mu.Lock()
	if l, ok := o.leads[channelID]; ok {
		l.session.Close()
		delete(o.leads, channelID)
	}
	o.mu.Unlock()
}

func (o *Orch) startLead(ctx context.Context, ch store.Channel, prompt string) error {
	mcpPath, err := o.writeLeadMCPConfig(ch)
	if err != nil {
		return err
	}
	sysPrompt := leadSystemPrompt(ch)
	if ch.Name == daemon.HomeChannelName {
		sysPrompt = homeSystemPrompt()
	}
	sess, err := o.harness.Start(ctx, agent.Spec{
		WorkDir:         o.d.Paths.ChannelDir(ch.Name),
		SystemPrompt:    sysPrompt,
		Prompt:          prompt,
		ResumeSessionID: ch.LeadSessionID,
		MCPConfigPath:   mcpPath,
		AllowedTools:    []string{"mcp__shipyard"},
	})
	if err != nil && ch.LeadSessionID != "" {
		// Stale session id (e.g. claude storage cleaned) — start fresh.
		sess, err = o.harness.Start(ctx, agent.Spec{
			WorkDir:       o.d.Paths.ChannelDir(ch.Name),
			SystemPrompt:  sysPrompt,
			Prompt:        prompt,
			MCPConfigPath: mcpPath,
			AllowedTools:  []string{"mcp__shipyard"},
		})
	}
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.leads[ch.ID] = &lead{session: sess, lastUse: time.Now()}
	o.mu.Unlock()
	go o.pumpLead(ch, sess)
	return nil
}

func (o *Orch) pumpLead(ch store.Channel, sess agent.Session) {
	for ev := range sess.Events() {
		switch ev.Kind {
		case agent.EvInit:
			if ev.SessionID != "" && ev.SessionID != ch.LeadSessionID {
				if err := o.d.Store.SetChannelLeadSession(ch.ID, ev.SessionID); err != nil {
					o.log.Error("persist lead session", "err", err)
				}
				ch.LeadSessionID = ev.SessionID
			}
		case agent.EvText:
			if _, err := o.d.PostMessage(store.Message{
				ChannelID: ch.ID, Author: "lead", Body: ev.Text,
			}); err != nil {
				o.log.Error("post lead message", "err", err)
			}
		case agent.EvResult:
			if ev.IsError {
				o.systemMessage(ch.ID, "", "lead turn errored: "+ev.Text)
			}
		case agent.EvExited:
			o.mu.Lock()
			delete(o.leads, ch.ID)
			o.mu.Unlock()
			return
		}
	}
}

func (o *Orch) reapIdleLeads() {
	for range time.Tick(time.Minute) {
		o.mu.Lock()
		for id, l := range o.leads {
			if time.Since(l.lastUse) > leadIdleTimeout {
				l.session.Close()
				delete(o.leads, id)
			}
		}
		o.mu.Unlock()
	}
}

// writeLeadMCPConfig writes the MCP config file pointing Claude at this
// channel's shipyard tool server (a subcommand of our own binary).
func (o *Orch) writeLeadMCPConfig(ch store.Channel) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	args := []string{"mcp-lead", "--channel", ch.ID}
	if ch.Name == daemon.HomeChannelName {
		args = append(args, "--home")
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"shipyard": map[string]any{"command": exe, "args": args},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	dir := o.d.Paths.ChannelDir(ch.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// --- extra RPC handlers (agent-facing + escape hatch) ---

func (o *Orch) registerHandlers() {
	srv := o.d.Server

	srv.Handle("task.create", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ChannelID string `json:"channel_id"`
			Repo      string `json:"repo"`
			Kind      string `json:"kind"`
			Title     string `json:"title"`
			Brief     string `json:"brief"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.createTask(context.WithoutCancel(ctx), p.ChannelID, p.Repo, p.Kind, p.Title, p.Brief)
	})

	srv.Handle("task.message", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TaskID string `json:"task_id"`
			Body   string `json:"body"`
			Author string `json:"author"` // lead (from MCP) — captain goes via message.send
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		t, err := o.d.Store.TaskByID(p.TaskID)
		if err != nil {
			return nil, err
		}
		if p.Author == "" {
			p.Author = "lead"
		}
		if _, err := o.d.PostMessage(store.Message{
			ChannelID: t.ChannelID, TaskID: t.ID, Author: p.Author, Body: p.Body,
		}); err != nil {
			return nil, err
		}
		if err := o.sendToCrew(context.WithoutCancel(ctx), t, p.Body); err != nil {
			return nil, fmt.Errorf("posted, but crewmate unreachable: %w", err)
		}
		return "sent", nil
	})

	srv.Handle("report.read", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		t, err := o.d.Store.TaskByID(p.TaskID)
		if err != nil {
			return nil, err
		}
		if t.ReportPath == "" {
			return nil, fmt.Errorf("task %s has no report", t.ID)
		}
		b, err := os.ReadFile(t.ReportPath)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	})

	srv.Handle("instructions.set", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ChannelID string `json:"channel_id"`
			Body      string `json:"body"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		ch, err := o.d.Store.ChannelByID(p.ChannelID)
		if err != nil {
			return nil, err
		}
		return "saved", os.WriteFile(ch.InstructionsPath, []byte(p.Body), 0o644)
	})

	srv.Handle("task.escape", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.escapeHatch(p.TaskID)
	})

	srv.Handle("task.handoff", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			TaskID   string `json:"task_id"`  // scout task with a report
			Proposal string `json:"proposal"` // proposed-task title to promote
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return o.handoff(context.WithoutCancel(ctx), p.TaskID, p.Proposal)
	})
}
