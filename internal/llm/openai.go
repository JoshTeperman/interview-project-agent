package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const openaiBaseURL = "https://api.openai.com/v1"

// openaiProvider calls the Responses API (/v1/responses) over net/http (S-D9).
// The Responses API is required for tool calls with reasoning enabled. Requests
// set store=false so nothing is kept server-side; reasoning items therefore come
// back encrypted and are passed back verbatim on later calls.
//
// It is hand-written rather than using the official Go SDK so the image builds
// within the VPS's memory (002-D3). Field names follow the SDK's type definitions.
type openaiProvider struct {
	http *http.Client
	opts Options
	base string
}

func newOpenAI(opts Options) *openaiProvider {
	base := openaiBaseURL
	if opts.BaseURL != "" {
		base = strings.TrimSuffix(opts.BaseURL, "/")
	}
	return &openaiProvider{http: &http.Client{Timeout: opts.Timeout}, opts: opts, base: base}
}

func (p *openaiProvider) Name() string  { return OpenAI }
func (p *openaiProvider) Model() string { return p.opts.Model }

type oaTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

func (p *openaiProvider) NewConversation(system string, tools []ToolDef) Conversation {
	ts := make([]oaTool, 0, len(tools))
	for _, t := range tools {
		ts = append(ts, oaTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Schema})
	}
	return &openaiConv{p: p, system: system, tools: ts}
}

type openaiConv struct {
	p       *openaiProvider
	system  string
	tools   []oaTool
	history []json.RawMessage // input items, append-only
	pending []ToolResult
}

func (c *openaiConv) AddToolResults(rs []ToolResult) { c.pending = append(c.pending, rs...) }

type oaRequest struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions"`
	Input           []json.RawMessage `json:"input"`
	Tools           []oaTool          `json:"tools"`
	ToolChoice      string            `json:"tool_choice"`
	MaxOutputTokens int64             `json:"max_output_tokens"`
	Store           bool              `json:"store"`
	Include         []string          `json:"include"`
}

type oaResponse struct {
	Status            string                   `json:"status"`
	IncompleteDetails *struct{ Reason string } `json:"incomplete_details"`
	Output            []json.RawMessage        `json:"output"`
	Usage             struct {
		InputTokens        int64 `json:"input_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

type oaItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (c *openaiConv) Send(ctx context.Context, text string, allowTools bool) (Reply, error) {
	input := c.history[:len(c.history):len(c.history)]
	for _, r := range c.pending {
		out := r.Content
		if r.IsError {
			out = "Error: " + out
		}
		input = append(input, mustJSON(map[string]string{"type": "function_call_output", "call_id": r.CallID, "output": out}))
	}
	if text != "" {
		input = append(input, mustJSON(map[string]string{"type": "message", "role": "user", "content": text}))
	}
	choice := "auto"
	if !allowTools {
		choice = "none"
	}
	body := mustJSON(oaRequest{
		Model: c.p.opts.Model, Instructions: c.system, Input: input, Tools: c.tools, ToolChoice: choice,
		MaxOutputTokens: c.p.opts.MaxOutputTokens, Store: false,
		Include: []string{"reasoning.encrypted_content"},
	})

	raw, status, err := postJSON(ctx, c.p.http, c.p.base+"/responses", map[string]string{"Authorization": "Bearer " + c.p.opts.APIKey}, body)
	if err != nil {
		return Reply{}, fmt.Errorf("openai: %w", err)
	}
	var resp oaResponse
	jsonErr := json.Unmarshal(raw, &resp)
	if status/100 != 2 {
		msg := http.StatusText(status)
		if jsonErr == nil && resp.Error != nil && resp.Error.Message != "" {
			msg = resp.Error.Message
		}
		ctxErr := jsonErr == nil && resp.Error != nil && resp.Error.Code == "context_length_exceeded"
		return Reply{}, &APIError{Provider: OpenAI, Status: status, Message: msg, ContextTooLong: ctxErr}
	}
	if jsonErr != nil {
		return Reply{}, fmt.Errorf("openai: decode response: %w", jsonErr)
	}

	reply := Reply{Usage: Usage{
		InputTokens:       resp.Usage.InputTokens,
		CachedInputTokens: resp.Usage.InputTokensDetails.CachedTokens,
		OutputTokens:      resp.Usage.OutputTokens,
	}}
	var sb strings.Builder
	refused := false
	for _, rawItem := range resp.Output {
		var it oaItem
		if err := json.Unmarshal(rawItem, &it); err != nil {
			return Reply{}, fmt.Errorf("openai: decode output item: %w", err)
		}
		// Every output item goes back into the history unchanged, as the SDK's ToParam does.
		input = append(input, rawItem)
		switch it.Type {
		case "message":
			for _, part := range it.Content {
				switch part.Type {
				case "output_text":
					sb.WriteString(part.Text)
				case "refusal":
					refused = true
				}
			}
		case "function_call":
			reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: it.CallID, Name: it.Name, Args: json.RawMessage(it.Arguments)})
		}
	}
	c.history = input
	c.pending = nil

	if refused && sb.Len() == 0 && len(reply.ToolCalls) == 0 {
		return reply, ErrRefused
	}
	reply.Truncated = resp.Status == "incomplete" && resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason == "max_output_tokens"
	reply.Text = sb.String()
	return reply, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only plain structs and maps are marshalled here
	}
	return b
}
