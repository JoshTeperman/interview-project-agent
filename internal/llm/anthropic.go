package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	anthropicBaseURL = "https://api.anthropic.com"
	anthropicVersion = "2026-10-21"
)

// anthropicProvider calls the Messages API (/v1/messages) over net/http with
// prompt caching (S-D9). It is hand-written rather than using the official SDK
// so the image builds within the VPS's memory (002-D3).
//
// The model's reply content is stored verbatim and sent back on later calls,
// so thinking blocks (with their signatures) are passed back unchanged.
type anthropicProvider struct {
	http *http.Client
	opts Options
	base string
}

func newAnthropic(opts Options) *anthropicProvider {
	base := anthropicBaseURL
	if opts.BaseURL != "" {
		base = strings.TrimSuffix(opts.BaseURL, "/")
	}
	return &anthropicProvider{http: &http.Client{Timeout: opts.Timeout}, opts: opts, base: base}
}

func (p *anthropicProvider) Name() string  { return Anthropic }
func (p *anthropicProvider) Model() string { return p.opts.Model }

type antTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type antCache struct {
	Type string `json:"type"`
}

var ephemeral = &antCache{Type: "ephemeral"}

type antText struct {
	Type         string    `json:"type"`
	Text         string    `json:"text"`
	CacheControl *antCache `json:"cache_control,omitempty"`
}

func (p *anthropicProvider) NewConversation(system string, tools []ToolDef) Conversation {
	ts := make([]antTool, 0, len(tools))
	for _, t := range tools {
		ts = append(ts, antTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	return &anthropicConv{p: p, tools: ts, system: []antText{{
		Type: "text", Text: system,
		// Caches tools + system together (they render first, in that order).
		CacheControl: ephemeral,
	}}}
}

type anthropicConv struct {
	p       *anthropicProvider
	tools   []antTool
	system  []antText
	history []json.RawMessage // messages, append-only
	pending []ToolResult
}

func (c *anthropicConv) AddToolResults(rs []ToolResult) { c.pending = append(c.pending, rs...) }

type antRequest struct {
	Model        string            `json:"model"`
	MaxTokens    int64             `json:"max_tokens"`
	System       []antText         `json:"system"`
	Tools        []antTool         `json:"tools"`
	ToolChoice   map[string]string `json:"tool_choice"`
	Messages     []json.RawMessage `json:"messages"`
	CacheControl *antCache         `json:"cache_control"`
}

type antResponse struct {
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      struct {
		InputTokens              int64 `json:"input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type antBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type antToolResult struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

func (c *anthropicConv) Send(ctx context.Context, text string, allowTools bool) (Reply, error) {
	var blocks []any
	for _, r := range c.pending {
		blocks = append(blocks, antToolResult{Type: "tool_result", ToolUseID: r.CallID, Content: r.Content, IsError: r.IsError})
	}
	if text != "" {
		blocks = append(blocks, antText{Type: "text", Text: text})
	}
	msgs := append(c.history[:len(c.history):len(c.history)], mustJSON(map[string]any{"role": "user", "content": blocks}))

	choice := "auto"
	if !allowTools {
		choice = "none"
	}
	body := mustJSON(antRequest{
		Model: c.p.opts.Model, MaxTokens: c.p.opts.MaxOutputTokens, System: c.system, Tools: c.tools,
		ToolChoice: map[string]string{"type": choice}, Messages: msgs,
		// Top-level breakpoint on the last block, so each step of the tool loop
		// and each later turn reads the conversation so far from the cache.
		CacheControl: ephemeral,
	})
	raw, status, err := postJSON(ctx, c.p.http, c.p.base+"/v1/messages", map[string]string{
		"x-api-key": c.p.opts.APIKey, "anthropic-version": anthropicVersion,
	}, body)
	if err != nil {
		return Reply{}, fmt.Errorf("anthropic: %w", err)
	}
	var resp antResponse
	jsonErr := json.Unmarshal(raw, &resp)
	if status/100 != 2 {
		msg := http.StatusText(status)
		if jsonErr == nil && resp.Error != nil && resp.Error.Message != "" {
			msg = resp.Error.Message
		}
		// "prompt is too long: N tokens > M maximum" (400), or a body over the request size limit (413).
		ctxErr := status == http.StatusRequestEntityTooLarge || (status == http.StatusBadRequest && strings.Contains(strings.ToLower(msg), "prompt is too long"))
		return Reply{}, &APIError{Provider: Anthropic, Status: status, Message: msg, ContextTooLong: ctxErr}
	}
	if jsonErr != nil {
		return Reply{}, fmt.Errorf("anthropic: decode response: %w", jsonErr)
	}
	var content []antBlock
	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return Reply{}, fmt.Errorf("anthropic: decode content: %w", err)
	}

	// The reply's content goes into history byte-for-byte (thinking blocks included).
	c.history = append(msgs, mustJSON(map[string]any{"role": "assistant", "content": resp.Content}))
	c.pending = nil

	u := resp.Usage
	reply := Reply{Usage: Usage{
		InputTokens:       u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
		CacheWriteTokens:  u.CacheCreationInputTokens,
		OutputTokens:      u.OutputTokens,
	}}
	if resp.StopReason == "refusal" {
		return reply, ErrRefused
	}
	reply.Truncated = resp.StopReason == "max_tokens"
	var sb strings.Builder
	for _, b := range content {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "tool_use":
			reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: b.Input})
		}
	}
	reply.Text = sb.String()
	return reply, nil
}
