package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/JoshTeperman/interview-project-agent/internal/agent"
	"github.com/JoshTeperman/interview-project-agent/internal/chat"
)

// maxChatBody allows MAX_MESSAGE_CHARS of multi-byte text plus JSON overhead.
const maxChatBody = 64 << 10

// EnableChat registers the protected chat routes. starters are the snapshot's
// starter questions (R11). M3 adds the streaming UI on top of these.
func (s *Server) EnableChat(c *chat.Service, starters []string) {
	s.chat = c
	if starters == nil {
		starters = []string{}
	}
	s.Protected.HandleFunc("GET /api/starters", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"starter_questions": starters})
	})
	s.Protected.HandleFunc("POST /api/chat", s.handleChat)
}

// sessionKey identifies a session to the chat service without handing it the cookie value.
func sessionKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	token, sess, _ := SessionFrom(r.Context())
	var req struct {
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &req, maxChatBody) {
		return
	}
	turn, err := s.chat.Ask(r.Context(), chat.Request{
		SessionKey: sessionKey(token), Email: sess.Email, IP: s.clientIP(r), Message: req.Message,
	}, func() bool { return s.auth.UseTurn(token) }, nil)

	var rl *chat.RateLimitedError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, chatResponse(turn, s.turnsRemaining(token)))
	case errors.Is(err, chat.ErrEmpty):
		writeError(w, http.StatusBadRequest, "empty_message", "Type a question first.")
	case errors.Is(err, chat.ErrTooLong):
		writeError(w, http.StatusBadRequest, "message_too_long", "That question is too long. Please shorten it.")
	case errors.As(err, &rl):
		writeRateLimited(w, rl.RetryAfter)
	case errors.Is(err, chat.ErrBusy):
		writeError(w, http.StatusConflict, "busy", "Still answering your last question. Wait for it to finish.")
	case errors.Is(err, chat.ErrTurnCap):
		writeError(w, http.StatusForbidden, "turn_cap_reached", "This session has used all its questions. Sign in again later or contact the candidate.")
	case errors.Is(err, chat.ErrRefused):
		writeError(w, http.StatusUnprocessableEntity, "model_refused", "The model declined to answer that question.")
	case errors.Is(err, chat.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, "llm_timeout", "The model took too long to answer. Please try again.")
	default:
		writeError(w, http.StatusBadGateway, "llm_error", "The model service had a problem. Please try again in a minute.")
	}
}

func (s *Server) turnsRemaining(token string) int {
	sess, ok := s.auth.Session(token)
	if !ok {
		return 0
	}
	return max(0, s.cfg.SessionTurnCap-sess.Turns)
}

func chatResponse(t agent.Turn, remaining int) map[string]any {
	steps := t.Steps
	if steps == nil {
		steps = []agent.Step{}
	}
	sources := t.Sources
	if sources == nil {
		sources = []string{}
	}
	return map[string]any{
		"answer": t.Answer, "sources": sources, "steps": steps, "cap_hit": t.CapHit,
		"truncated": t.Truncated, "turns_remaining": remaining,
	}
}
