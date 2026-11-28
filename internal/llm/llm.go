// Package llm is the LLMClient layer (S-D9): a Provider interface with one
// implementation per backend, so the agent loop never sees a vendor API.
//
// A Conversation keeps its history in the provider's own message format and only
// ever appends to it. Rewriting earlier turns (for example dropping old tool
// results or reasoning items) would invalidate prompt caches and, on models with
// preserved reasoning, the model's earlier thinking.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Provider names, matching LLM_PROVIDER.
const (
	OpenAI    = "openai"
	Anthropic = "anthropic"
)

// ErrRefused means the model declined to answer (a safety refusal).
var ErrRefused = errors.New("the model declined to answer")

// ToolDef describes one tool the model may call.
type ToolDef struct {
	Name        string
	Description string
	// Schema is a JSON Schema object for the tool's arguments.
	Schema map[string]any
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Usage is token usage for one or more model calls.
type Usage struct {
	InputTokens       int64 `json:"input_tokens"`        // all input, including cached
	CachedInputTokens int64 `json:"cached_input_tokens"` // input read from the prompt cache
	CacheWriteTokens  int64 `json:"cache_write_tokens"`  // input written to the cache (Anthropic)
	OutputTokens      int64 `json:"output_tokens"`       // includes reasoning tokens
}

// Add accumulates u2 into u.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.CachedInputTokens += u2.CachedInputTokens
	u.CacheWriteTokens += u2.CacheWriteTokens
	u.OutputTokens += u2.OutputTokens
}

// Reply is the model's answer to one Send.
type Reply struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
	// Truncated is set when the reply hit the output token limit.
	Truncated bool
}

// Conversation is one append-only chat thread with a model.
type Conversation interface {
	// AddToolResults queues results for the model's last tool calls. They are
	// sent at the start of the next Send.
	AddToolResults(results []ToolResult)
	// Send sends any queued tool results plus text (may be empty after tool
	// results) and appends the model's reply to the history. With allowTools
	// false the model must answer in text. On error the history is unchanged
	// and queued results stay queued.
	Send(ctx context.Context, text string, allowTools bool) (Reply, error)
}

// Provider is one LLM backend.
type Provider interface {
	Name() string
	Model() string
	// NewConversation starts a thread with a fixed system prompt and tool set.
	// Both stay identical for the thread's life, so they cache.
	NewConversation(system string, tools []ToolDef) Conversation
}

// Options configures a provider.
type Options struct {
	Provider        string // OpenAI or Anthropic
	Model           string
	APIKey          string
	MaxOutputTokens int64
	Timeout         time.Duration // per model call
	BaseURL         string        // tests only; empty means the vendor default
}

// New returns the provider named in opts.
func New(opts Options) (Provider, error) {
	if opts.APIKey == "" || opts.Model == "" || opts.MaxOutputTokens <= 0 || opts.Timeout <= 0 {
		return nil, errors.New("llm: api key, model, max output tokens, and timeout are required")
	}
	switch opts.Provider {
	case OpenAI:
		return newOpenAI(opts), nil
	case Anthropic:
		return newAnthropic(opts), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", opts.Provider)
	}
}
