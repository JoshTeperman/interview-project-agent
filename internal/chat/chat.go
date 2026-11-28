// Package chat turns a signed-in interviewer's question into an agent turn:
// it validates the message, applies chat limits (R10), keeps one append-only
// conversation per session, and logs the question (R11).
//
// A conversation only grows, so before a question that could push it past
// ContextMaxInputTokens the session starts a fresh one, and says so.
package chat

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JoshTeperman/interview-project-agent/internal/agent"
	"github.com/JoshTeperman/interview-project-agent/internal/llm"
	"github.com/JoshTeperman/interview-project-agent/internal/questionlog"
	"github.com/JoshTeperman/interview-project-agent/internal/ratelimit"
	"github.com/JoshTeperman/interview-project-agent/prompts"
)

// Errors returned by Ask. The HTTP layer maps them to UI states (U2).
var (
	ErrEmpty    = errors.New("empty message")
	ErrTooLong  = errors.New("message too long")
	ErrBusy     = errors.New("a question is already in progress for this session")
	ErrTurnCap  = errors.New("session turn cap reached")
	ErrTimeout  = errors.New("the model took too long to answer")
	ErrUpstream = errors.New("the model provider returned an error")
	// ErrContextFull means even a fresh conversation was too long for the model.
	ErrContextFull = errors.New("conversation too long for the model")
	ErrRefused     = llm.ErrRefused
)

// RateLimitedError reports that a chat limit was hit and when to retry.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "rate limited" }

// Options configures limits.
type Options struct {
	PerSessionMinute int
	PerIPMinute      int
	MaxMessageChars  int
	IdleTTL          time.Duration // threads idle this long are dropped
	QuestionTimeout  time.Duration // bounds one whole question
	// ContextMaxInputTokens caps a conversation's input size. A fresh conversation
	// starts when the last input size plus TurnTokenBudget would exceed it.
	ContextMaxInputTokens int
	// TurnTokenBudget is a worst-case estimate of the tokens one question adds.
	TurnTokenBudget int
}

// TurnTokenBudget estimates the most input one question can add: the message
// plus every tool result at its cap, at a conservative 3 characters per token.
func TurnTokenBudget(stepCap, toolOutputMaxChars, maxMessageChars int) int {
	return (stepCap*toolOutputMaxChars + maxMessageChars) / 3
}

// Answer is a completed question.
type Answer struct {
	agent.Turn
	// ConversationReset is set when this question started a fresh conversation
	// because the previous one was close to the context limit.
	ConversationReset bool `json:"conversation_reset"`
}

// Service answers questions for signed-in sessions.
type Service struct {
	agent    *agent.Agent
	provider llm.Provider
	qlog     *questionlog.Logger
	log      *slog.Logger
	opts     Options
	now      func() time.Time

	perSession, perIP *ratelimit.Limiter

	mu      sync.Mutex
	threads map[string]*thread // by session key
}

type thread struct {
	mu        sync.Mutex // held for a whole turn
	conv      llm.Conversation
	last      time.Time
	lastInput int64 // input tokens of the last model call
}

// New returns a Service. qlog may be nil. now may be nil (time.Now).
func New(a *agent.Agent, p llm.Provider, qlog *questionlog.Logger, log *slog.Logger, opts Options, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		agent: a, provider: p, qlog: qlog, log: log, opts: opts, now: now,
		perSession: ratelimit.New(opts.PerSessionMinute, time.Minute).WithClock(now),
		perIP:      ratelimit.New(opts.PerIPMinute, time.Minute).WithClock(now),
		threads:    map[string]*thread{},
	}
}

// Request is one question from a signed-in session.
type Request struct {
	SessionKey string // stable per session, not the cookie value itself
	Email      string
	IP         string
	Message    string
}

// Ask validates req, applies limits, and runs one agent turn in the session's
// conversation. useTurn is called once the request passes every other check;
// it returns false when the session's turn cap is spent. onStep may be nil.
func (s *Service) Ask(ctx context.Context, req Request, useTurn func() bool, onStep func(agent.Step)) (Answer, error) {
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		return Answer{}, ErrEmpty
	}
	if utf8.RuneCountInString(msg) > s.opts.MaxMessageChars {
		return Answer{}, ErrTooLong
	}
	if ok, retry := s.perIP.Allow(req.IP); !ok {
		return Answer{}, &RateLimitedError{retry}
	}
	if ok, retry := s.perSession.Allow(req.SessionKey); !ok {
		return Answer{}, &RateLimitedError{retry}
	}

	th := s.thread(req.SessionKey)
	if !th.mu.TryLock() {
		return Answer{}, ErrBusy
	}
	defer th.mu.Unlock()
	if !useTurn() {
		return Answer{}, ErrTurnCap
	}

	var ans Answer
	if th.lastInput > 0 && th.lastInput+int64(s.opts.TurnTokenBudget) > int64(s.opts.ContextMaxInputTokens) {
		s.reset(th)
		ans.ConversationReset = true
	}
	if s.opts.QuestionTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.QuestionTimeout)
		defer cancel()
	}

	start := s.now()
	turn, err := s.agent.Ask(ctx, th.conv, msg, onStep)
	if errors.Is(err, llm.ErrContextLength) && !ans.ConversationReset {
		// The estimate missed; the model call was refused, not billed. Retry once fresh.
		s.reset(th)
		ans.ConversationReset = true
		turn, err = s.agent.Ask(ctx, th.conv, msg, onStep)
	}
	latency := time.Since(start)
	th.last = s.now()
	ans.Turn = turn
	if err == nil {
		th.lastInput = turn.LastInputTokens
	}

	entry := questionlog.Entry{
		Time: start.UTC(), Email: req.Email, Provider: s.provider.Name(), Model: s.provider.Model(),
		Prompt: prompts.Version(), Question: msg, Steps: len(turn.Steps), CapHit: turn.CapHit,
		Sources: turn.Sources, Usage: turn.Usage, LatencyMS: latency.Milliseconds(),
		ConversationReset: ans.ConversationReset,
	}
	err = classify(ctx, err)
	if err != nil {
		entry.Error = err.Error()
	}
	if lerr := s.qlog.Log(entry); lerr != nil {
		s.log.Error("question_log_failed", "error", lerr.Error())
	}
	attrs := []any{"email", req.Email, "tool_calls", len(turn.Steps), "llm_calls", turn.LLMCalls, "cap_hit", turn.CapHit,
		"conversation_reset", ans.ConversationReset, "last_input_tokens", turn.LastInputTokens,
		"input_tokens", turn.Usage.InputTokens, "cached_input_tokens", turn.Usage.CachedInputTokens,
		"output_tokens", turn.Usage.OutputTokens, "latency_ms", latency.Milliseconds()}
	if err != nil {
		s.log.Error("chat_turn_failed", append(attrs, "error", err.Error())...)
		return ans, err
	}
	s.log.Info("chat_turn", attrs...)
	return ans, nil
}

// reset replaces th's conversation with a fresh one. The caller holds th.mu.
func (s *Service) reset(th *thread) {
	th.conv = s.agent.NewConversation(s.provider)
	th.lastInput = 0
}

// classify maps provider and context errors to the package's errors, keeping
// the original message (which never contains the API key) for logs.
func classify(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, llm.ErrRefused):
		return ErrRefused
	case errors.Is(err, llm.ErrContextLength):
		return errors.Join(ErrContextFull, err)
	case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil, isTimeout(err):
		return errors.Join(ErrTimeout, err)
	default:
		return errors.Join(ErrUpstream, err)
	}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

func (s *Service) thread(key string) *thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	th, ok := s.threads[key]
	if !ok {
		th = &thread{conv: s.agent.NewConversation(s.provider), last: s.now()}
		s.threads[key] = th
	}
	return th
}

// Forget drops a session's conversation, for logout.
func (s *Service) Forget(key string) {
	s.mu.Lock()
	delete(s.threads, key)
	s.mu.Unlock()
}

// Prune drops idle conversations and expired rate-limit buckets.
func (s *Service) Prune() {
	cutoff := s.now().Add(-s.opts.IdleTTL)
	s.mu.Lock()
	for k, th := range s.threads {
		if th.mu.TryLock() {
			if th.last.Before(cutoff) {
				delete(s.threads, k)
			}
			th.mu.Unlock()
		}
	}
	s.mu.Unlock()
	s.perSession.Prune()
	s.perIP.Prune()
}

// RunPruner calls Prune every interval until ctx ends.
func (s *Service) RunPruner(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Prune()
		}
	}
}
