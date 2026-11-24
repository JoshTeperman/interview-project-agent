package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JoshTeperman/interview-project-agent/internal/agent"
	"github.com/JoshTeperman/interview-project-agent/internal/auth"
	"github.com/JoshTeperman/interview-project-agent/internal/chat"
	"github.com/JoshTeperman/interview-project-agent/internal/config"
	"github.com/JoshTeperman/interview-project-agent/internal/llm"
	"github.com/JoshTeperman/interview-project-agent/internal/questionlog"
	"github.com/JoshTeperman/interview-project-agent/internal/snapshot"
)

// fakeProvider answers by reading docs/spec.md once, then replying with reply.
type fakeProvider struct {
	mu    sync.Mutex
	reply string
	err   error
	block chan struct{} // if set, Send waits on it
	convs int
}

func (p *fakeProvider) Name() string  { return "fake" }
func (p *fakeProvider) Model() string { return "fake-model" }
func (p *fakeProvider) NewConversation(string, []llm.ToolDef) llm.Conversation {
	p.mu.Lock()
	p.convs++
	p.mu.Unlock()
	return &fakeConv{p: p}
}

type fakeConv struct {
	p       *fakeProvider
	pending []llm.ToolResult
}

func (c *fakeConv) AddToolResults(rs []llm.ToolResult) { c.pending = append(c.pending, rs...) }
func (c *fakeConv) Send(ctx context.Context, text string, _ bool) (llm.Reply, error) {
	if c.p.block != nil {
		<-c.p.block
	}
	if c.p.err != nil {
		return llm.Reply{}, c.p.err
	}
	if text != "" {
		return llm.Reply{ToolCalls: []llm.ToolCall{{ID: "1", Name: agent.ToolRead, Args: json.RawMessage(`{"path":"docs/spec.md"}`)}}}, nil
	}
	c.pending = nil
	return llm.Reply{Text: c.p.reply, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

type chatHarness struct {
	*harness
	prov    *fakeProvider
	dataDir string
}

func newChatHarness(t *testing.T, turnCap int) *chatHarness {
	t.Helper()
	cfg := &config.Config{
		AllowedDomains: []string{"example.com"}, CodeTTL: 10 * time.Minute, SessionTTL: time.Hour,
		CodeRequestsPerIPHour: 50, CodeRequestsPerEmailHour: 10, VerifyAttemptsPerCode: 5, VerifyPerIPHour: 50,
		SessionTurnCap: turnCap, ChatPerSessionMinute: 3, ChatPerIPMinute: 100, MaxMessageChars: 50,
	}
	m, logs := &fakeMail{}, &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	a := auth.New(cfg, m, log, nil)
	s := New(cfg, a, log)

	snap, err := snapshot.Open("../../evaluation/fixtures/tiny-kv", snapshot.Limits{MaxToolChars: 8000, MaxOverviewChars: 30000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { snap.Close() })
	dataDir := t.TempDir()
	qlog, _ := questionlog.Open(dataDir)
	t.Cleanup(func() { qlog.Close() })
	prov := &fakeProvider{reply: "Append-only log (docs/spec.md)."}
	c := chat.New(agent.New(snap, 6), prov, qlog, log, chat.Options{
		PerSessionMinute: cfg.ChatPerSessionMinute, PerIPMinute: cfg.ChatPerIPMinute, MaxMessageChars: cfg.MaxMessageChars, IdleTTL: time.Hour,
	}, nil)
	s.EnableChat(c, snap.Project.StarterQuestions)
	return &chatHarness{harness: &harness{h: s.Handler(), mail: m, logs: logs, srv: s, auth: a}, prov: prov, dataDir: dataDir}
}

func decode(t *testing.T, b *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b.Bytes(), &m); err != nil {
		t.Fatalf("bad JSON %q: %v", b, err)
	}
	return m
}

func TestChatAnswersWithSourcesAndLogsQuestion(t *testing.T) {
	h := newChatHarness(t, 40)
	c := h.login(t, "rev@example.com")
	w := h.do("POST", "/api/chat", `{"message":"Why a log? my code is 123456"}`, c)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	resp := decode(t, w.Body)
	if resp["answer"] != "Append-only log (docs/spec.md)." || resp["turns_remaining"] != 39.0 {
		t.Fatalf("resp = %v", resp)
	}
	if src, _ := resp["sources"].([]any); len(src) != 1 || src[0] != "docs/spec.md" {
		t.Errorf("sources = %v", resp["sources"])
	}
	if steps, _ := resp["steps"].([]any); len(steps) != 1 {
		t.Errorf("steps = %v", resp["steps"])
	}

	b, err := os.ReadFile(filepath.Join(h.dataDir, questionlog.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var e questionlog.Entry
	json.Unmarshal(bytes.TrimSpace(b), &e)
	if e.Question != "Why a log? my code is [redacted]" || e.Email != "rev@example.com" || e.Steps != 1 || e.Prompt == "" {
		t.Fatalf("logged = %+v", e)
	}
	logs := h.logs.String()
	if strings.Contains(logs, "Why a log") || strings.Contains(logs, c.Value) || !strings.Contains(logs, `"msg":"chat_turn"`) {
		t.Fatalf("server log: %s", logs)
	}
}

func TestStartersNeedAuth(t *testing.T) {
	h := newChatHarness(t, 40)
	if w := h.do("GET", "/api/starters", "", nil); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	if w := h.do("POST", "/api/chat", `{"message":"hi"}`, nil); w.Code != 401 || strings.Contains(w.Body.String(), "Append-only") {
		t.Fatalf("unauthenticated chat: %d %s", w.Code, w.Body)
	}
	c := h.login(t, "a@example.com")
	w := h.do("GET", "/api/starters", "", c)
	qs, _ := decode(t, w.Body)["starter_questions"].([]any)
	if w.Code != 200 || len(qs) != 3 {
		t.Fatalf("%d %v", w.Code, qs)
	}
}

func TestChatErrorStates(t *testing.T) {
	h := newChatHarness(t, 2)
	c := h.login(t, "a@example.com")
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"message":"   "}`, 400, "empty_message"},
		{`{"message":"` + strings.Repeat("x", 51) + `"}`, 400, "message_too_long"},
		{`{"message":"one"}`, 200, ""},
		{`{"message":"two"}`, 200, ""},
		{`{"message":"three"}`, 403, "turn_cap_reached"},
		{`{"message":"four"}`, 429, "rate_limited"}, // 3 per session per minute
	} {
		w := h.do("POST", "/api/chat", tc.body, c)
		if w.Code != tc.status || (tc.code != "" && decode(t, w.Body)["error"] != tc.code) {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body)
		}
	}
}

func TestChatProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&llm.APIError{Provider: "openai", Status: 500, Message: "boom"}, 502, "llm_error"},
		{context.DeadlineExceeded, 504, "llm_timeout"},
		{llm.ErrRefused, 422, "model_refused"},
	} {
		h := newChatHarness(t, 40)
		h.prov.err = tc.err
		c := h.login(t, "a@example.com")
		w := h.do("POST", "/api/chat", `{"message":"q"}`, c)
		if w.Code != tc.status || decode(t, w.Body)["error"] != tc.code {
			t.Errorf("%v: %d %s", tc.err, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "boom") {
			t.Errorf("upstream detail leaked to client: %s", w.Body)
		}
	}
}

func TestOneQuestionAtATimePerSession(t *testing.T) {
	h := newChatHarness(t, 40)
	h.prov.block = make(chan struct{})
	c := h.login(t, "a@example.com")
	done := make(chan int)
	go func() { done <- h.do("POST", "/api/chat", `{"message":"slow"}`, c).Code }()
	var w2 int
	for i := 0; i < 100; i++ { // wait until the first request holds the session
		time.Sleep(5 * time.Millisecond)
		if w2 = h.do("POST", "/api/chat", `{"message":"again"}`, c).Code; w2 == 409 {
			break
		}
	}
	close(h.prov.block)
	if first := <-done; first != 200 || w2 != 409 {
		t.Fatalf("first=%d second=%d", first, w2)
	}
}

func TestLogoutDropsConversation(t *testing.T) {
	h := newChatHarness(t, 40)
	c := h.login(t, "a@example.com")
	h.do("POST", "/api/chat", `{"message":"q"}`, c)
	h.do("POST", "/api/auth/logout", "", c)
	c2 := h.login(t, "a@example.com")
	h.do("POST", "/api/chat", `{"message":"q"}`, c2)
	if h.prov.convs != 2 {
		t.Fatalf("conversations = %d, want a fresh one after logout", h.prov.convs)
	}
}
