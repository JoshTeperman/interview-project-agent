package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI records request bodies and replies with canned JSON, in order.
type fakeAPI struct {
	mu      sync.Mutex
	path    string
	bodies  []map[string]any
	headers []http.Header
	replies []string
	status  int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != f.path {
		http.Error(w, "unexpected path "+r.URL.Path, 404)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	f.bodies = append(f.bodies, m)
	f.headers = append(f.headers, r.Header.Clone())
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	i := len(f.bodies) - 1
	if i >= len(f.replies) {
		i = len(f.replies) - 1
	}
	io.WriteString(w, f.replies[i])
}

var testTools = []ToolDef{{
	Name: "read_file", Description: "Read a file",
	Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}},
}}

func newProvider(t *testing.T, name string, f *fakeAPI) Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p, err := New(Options{Provider: name, Model: "test-model", APIKey: "sk-test-SECRETKEY", MaxOutputTokens: 1000, Timeout: 5 * time.Second, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func get(m any, path ...any) any {
	for _, k := range path {
		switch k := k.(type) {
		case string:
			mm, _ := m.(map[string]any)
			m = mm[k]
		case int:
			s, _ := m.([]any)
			if k >= len(s) {
				return nil
			}
			m = s[k]
		}
	}
	return m
}

func TestAnthropicToolRoundTripAndCaching(t *testing.T) {
	f := &fakeAPI{path: "/v1/messages", replies: []string{
		`{"id":"m1","type":"message","role":"assistant","model":"test-model","stop_reason":"tool_use",
		  "content":[{"type":"thinking","thinking":"","signature":"sig-1"},
		             {"type":"text","text":"Let me look."},
		             {"type":"tool_use","id":"tu_1","name":"read_file","input":{"path":"docs/spec.md"}}],
		  "usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":900,"output_tokens":20}}`,
		`{"id":"m2","type":"message","role":"assistant","model":"test-model","stop_reason":"end_turn",
		  "content":[{"type":"text","text":"It uses an append-only log (docs/spec.md)."}],
		  "usage":{"input_tokens":30,"cache_read_input_tokens":900,"cache_creation_input_tokens":50,"output_tokens":15}}`,
	}}
	p := newProvider(t, Anthropic, f)
	conv := p.NewConversation("SYSTEM PROMPT", testTools)

	r1, err := conv.Send(context.Background(), "Why a log?", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.ToolCalls) != 1 || r1.ToolCalls[0].ID != "tu_1" || r1.ToolCalls[0].Name != "read_file" || !strings.Contains(string(r1.ToolCalls[0].Args), "docs/spec.md") {
		t.Fatalf("tool calls = %+v", r1.ToolCalls)
	}
	if r1.Usage.InputTokens != 910 || r1.Usage.CacheWriteTokens != 900 {
		t.Errorf("usage = %+v", r1.Usage)
	}
	conv.AddToolResults([]ToolResult{{CallID: "tu_1", Content: "spec text"}})
	r2, err := conv.Send(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Text != "It uses an append-only log (docs/spec.md)." || r2.Usage.CachedInputTokens != 900 {
		t.Fatalf("r2 = %+v", r2)
	}

	b1, b2 := f.bodies[0], f.bodies[1]
	h := f.headers[0]
	if (h.Get("X-Api-Key") != "sk-test-SECRETKEY" && h.Get("Authorization") != "Bearer sk-test-SECRETKEY") || h.Get("Anthropic-Version") == "" {
		t.Errorf("headers = %v", f.headers[0])
	}
	if get(b1, "model") != "test-model" || get(b1, "max_tokens") != 1000.0 {
		t.Errorf("model/max_tokens: %v", b1)
	}
	if get(b1, "system", 0, "text") != "SYSTEM PROMPT" || get(b1, "system", 0, "cache_control", "type") != "ephemeral" {
		t.Errorf("system not cached: %v", get(b1, "system"))
	}
	if get(b1, "cache_control", "type") != "ephemeral" {
		t.Errorf("no top-level cache breakpoint: %v", b1)
	}
	if get(b1, "tools", 0, "name") != "read_file" || get(b1, "tools", 0, "input_schema", "properties", "path", "type") != "string" {
		t.Errorf("tools = %v", get(b1, "tools"))
	}
	if get(b1, "tool_choice", "type") != "auto" || get(b2, "tool_choice", "type") != "none" {
		t.Errorf("tool_choice = %v / %v", get(b1, "tool_choice"), get(b2, "tool_choice"))
	}
	// Second request: the first request's messages unchanged, then the assistant
	// reply verbatim (thinking block included), then the tool result.
	msgs := get(b2, "messages").([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	m0a, _ := json.Marshal(get(b1, "messages", 0))
	m0b, _ := json.Marshal(msgs[0])
	if string(m0a) != string(m0b) {
		t.Errorf("history was rewritten:\n%s\n%s", m0a, m0b)
	}
	if get(msgs[1], "role") != "assistant" || get(msgs[1], "content", 0, "type") != "thinking" || get(msgs[1], "content", 0, "signature") != "sig-1" {
		t.Errorf("assistant turn not echoed verbatim: %v", msgs[1])
	}
	tr := get(msgs[2], "content", 0)
	if get(tr, "type") != "tool_result" || get(tr, "tool_use_id") != "tu_1" {
		t.Errorf("tool result = %v", tr)
	}
}

func TestAnthropicRefusalAndErrors(t *testing.T) {
	f := &fakeAPI{path: "/v1/messages", replies: []string{
		`{"id":"m1","type":"message","role":"assistant","model":"test-model","stop_reason":"refusal","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
	}}
	conv := newProvider(t, Anthropic, f).NewConversation("s", testTools)
	if _, err := conv.Send(context.Background(), "q", true); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
	bad := &fakeAPI{path: "/v1/messages", status: 400, replies: []string{`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`}}
	conv2 := newProvider(t, Anthropic, bad).NewConversation("s", testTools)
	_, err := conv2.Send(context.Background(), "q", true)
	if err == nil || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIToolRoundTrip(t *testing.T) {
	f := &fakeAPI{path: "/responses", replies: []string{
		`{"id":"r1","status":"completed","output":[
		   {"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC-1"},
		   {"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"docs/spec.md\"}","status":"completed"}],
		 "usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1024},"output_tokens":40,"output_tokens_details":{"reasoning_tokens":30},"total_tokens":1240}}`,
		`{"id":"r2","status":"completed","output":[
		   {"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Append-only (docs/spec.md).","annotations":[]}]}],
		 "usage":{"input_tokens":1300,"input_tokens_details":{"cached_tokens":1200},"output_tokens":10,"total_tokens":1310}}`,
	}}
	conv := newProvider(t, OpenAI, f).NewConversation("SYSTEM PROMPT", testTools)
	r1, err := conv.Send(context.Background(), "Why a log?", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.ToolCalls) != 1 || r1.ToolCalls[0].ID != "call_1" || string(r1.ToolCalls[0].Args) != `{"path":"docs/spec.md"}` {
		t.Fatalf("calls = %+v", r1.ToolCalls)
	}
	if r1.Usage.InputTokens != 1200 || r1.Usage.CachedInputTokens != 1024 || r1.Usage.OutputTokens != 40 {
		t.Errorf("usage = %+v", r1.Usage)
	}
	conv.AddToolResults([]ToolResult{{CallID: "call_1", Content: "nope", IsError: true}})
	r2, err := conv.Send(context.Background(), "", false)
	if err != nil || r2.Text != "Append-only (docs/spec.md)." {
		t.Fatalf("r2 = %+v err=%v", r2, err)
	}

	b1, b2 := f.bodies[0], f.bodies[1]
	if f.headers[0].Get("Authorization") != "Bearer sk-test-SECRETKEY" {
		t.Errorf("auth header = %q", f.headers[0].Get("Authorization"))
	}
	if get(b1, "store") != false || get(b1, "include", 0) != "reasoning.encrypted_content" || get(b1, "instructions") != "SYSTEM PROMPT" {
		t.Errorf("b1 = %v", b1)
	}
	if get(b1, "tools", 0, "type") != "function" || get(b1, "tools", 0, "name") != "read_file" || get(b1, "tools", 0, "parameters", "type") != "object" {
		t.Errorf("tools = %v", get(b1, "tools"))
	}
	if get(b1, "tool_choice") != "auto" || get(b2, "tool_choice") != "none" || get(b1, "max_output_tokens") != 1000.0 {
		t.Errorf("choice/max: %v %v", b1, b2)
	}
	if get(b1, "input", 0, "role") != "user" || get(b1, "input", 0, "content") != "Why a log?" {
		t.Errorf("input = %v", get(b1, "input"))
	}
	in2 := get(b2, "input").([]any)
	if len(in2) != 4 || get(in2[1], "encrypted_content") != "ENC-1" || get(in2[2], "call_id") != "call_1" {
		t.Fatalf("history not echoed: %v", in2)
	}
	if get(in2[3], "type") != "function_call_output" || get(in2[3], "call_id") != "call_1" || get(in2[3], "output") != "Error: nope" {
		t.Errorf("tool output = %v", in2[3])
	}
}

func TestOpenAIErrorsDontLeakKeyAndKeepHistory(t *testing.T) {
	f := &fakeAPI{path: "/responses", status: 429, replies: []string{`{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`}}
	conv := newProvider(t, OpenAI, f).NewConversation("s", testTools)
	_, err := conv.Send(context.Background(), "q", true)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("err = %v", err)
	}
	if n := len(conv.(*openaiConv).history); n != 0 {
		t.Fatalf("failed send changed history: %d items", n)
	}
}

func TestOpenAITruncatedAndRefusal(t *testing.T) {
	f := &fakeAPI{path: "/responses", replies: []string{
		`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{}}`,
		`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"no"}]}],"usage":{}}`,
	}}
	conv := newProvider(t, OpenAI, f).NewConversation("s", testTools)
	r, err := conv.Send(context.Background(), "q", true)
	if err != nil || !r.Truncated || r.Text != "partial" {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if _, err := conv.Send(context.Background(), "q2", true); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{Provider: "xai", Model: "m", APIKey: "k", MaxOutputTokens: 1, Timeout: time.Second}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := New(Options{Provider: OpenAI, Model: "m", MaxOutputTokens: 1, Timeout: time.Second}); err == nil {
		t.Fatal("missing key accepted")
	}
}
