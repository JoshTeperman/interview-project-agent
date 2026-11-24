package llm

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// anthropicProvider calls the Messages API with prompt caching (S-D9).
type anthropicProvider struct {
	client anthropic.Client
	opts   Options
}

func newAnthropic(opts Options) *anthropicProvider {
	ro := []option.RequestOption{option.WithAPIKey(opts.APIKey), option.WithRequestTimeout(opts.Timeout)}
	if opts.BaseURL != "" {
		ro = append(ro, option.WithBaseURL(opts.BaseURL))
	}
	return &anthropicProvider{client: anthropic.NewClient(ro...), opts: opts}
}

func (p *anthropicProvider) Name() string  { return Anthropic }
func (p *anthropicProvider) Model() string { return p.opts.Model }

func (p *anthropicProvider) NewConversation(system string, tools []ToolDef) Conversation {
	ts := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		props, _ := t.Schema["properties"].(map[string]any)
		req, _ := t.Schema["required"].([]string)
		tool := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{Properties: props, Required: req},
		}
		ts = append(ts, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return &anthropicConv{p: p, tools: ts, system: []anthropic.TextBlockParam{{
		Text: system,
		// Caches tools + system together (they render first, in that order).
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}}}
}

type anthropicConv struct {
	p       *anthropicProvider
	tools   []anthropic.ToolUnionParam
	system  []anthropic.TextBlockParam
	history []anthropic.MessageParam
	pending []ToolResult
}

func (c *anthropicConv) AddToolResults(rs []ToolResult) { c.pending = append(c.pending, rs...) }

func (c *anthropicConv) Send(ctx context.Context, text string, allowTools bool) (Reply, error) {
	var blocks []anthropic.ContentBlockParamUnion
	for _, r := range c.pending {
		blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
	}
	if text != "" {
		blocks = append(blocks, anthropic.NewTextBlock(text))
	}
	msgs := append(c.history[:len(c.history):len(c.history)], anthropic.NewUserMessage(blocks...))

	choice := anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	if !allowTools {
		choice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
	}
	resp, err := c.p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.p.opts.Model),
		MaxTokens: c.p.opts.MaxOutputTokens,
		System:    c.system,
		Tools:     c.tools,
		Messages:  msgs,
		// Top-level breakpoint on the last block, so each step of the tool loop
		// and each later turn reads the conversation so far from the cache.
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
		ToolChoice:   choice,
	})
	if err != nil {
		return Reply{}, err
	}
	// The reply goes into history unchanged (thinking blocks included).
	c.history = append(msgs, resp.ToParam())
	c.pending = nil

	u := resp.Usage
	reply := Reply{Usage: Usage{
		InputTokens:       u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
		CacheWriteTokens:  u.CacheCreationInputTokens,
		OutputTokens:      u.OutputTokens,
	}}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return reply, ErrRefused
	}
	reply.Truncated = resp.StopReason == anthropic.StopReasonMaxTokens
	var sb strings.Builder
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			sb.WriteString(b.Text)
		case anthropic.ToolUseBlock:
			reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: json.RawMessage(b.JSON.Input.Raw())})
		}
	}
	reply.Text = sb.String()
	return reply, nil
}
