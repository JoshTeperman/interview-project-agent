// Package questionlog records interviewers' questions for debrief prep (S-D6, R11).
//
// Each question is one JSON line in DATA_DIR/questions.jsonl. Question text is
// masked first: configured secrets, anything shaped like an API key or token,
// and 6-digit numbers (which could be magic codes) are replaced (R3).
package questionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JoshTeperman/interview-project-agent/internal/llm"
)

// FileName is the log file inside DATA_DIR.
const FileName = "questions.jsonl"

const redacted = "[redacted]"

// Entry is one logged question.
type Entry struct {
	Time     time.Time `json:"time"`
	Email    string    `json:"email"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Prompt   string    `json:"prompt_version"`
	Question string    `json:"question"`
	Steps    int       `json:"tool_calls"`
	CapHit   bool      `json:"cap_hit,omitempty"`
	// ConversationReset: this question started a fresh conversation (context limit).
	ConversationReset bool      `json:"conversation_reset,omitempty"`
	Sources           []string  `json:"sources,omitempty"`
	Usage             llm.Usage `json:"usage"`
	LatencyMS         int64     `json:"latency_ms"`
	Error             string    `json:"error,omitempty"`
}

// Logger appends entries to the question log. A nil *Logger discards entries.
type Logger struct {
	mu      sync.Mutex
	f       *os.File
	secrets []string
}

// Open opens (or creates, mode 0600) the log in dataDir. An empty dataDir
// returns a nil Logger, which logs nothing. secrets are exact values to mask.
func Open(dataDir string, secrets ...string) (*Logger, error) {
	if dataDir == "" {
		return nil, nil
	}
	f, err := os.OpenFile(filepath.Join(dataDir, FileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	var keep []string
	for _, s := range secrets {
		if len(s) >= 4 {
			keep = append(keep, s)
		}
	}
	return &Logger{f: f, secrets: keep}, nil
}

// Log masks e.Question and e.Error and appends e. Write errors are returned
// but never contain the question.
func (l *Logger) Log(e Entry) error {
	if l == nil {
		return nil
	}
	e.Question = Mask(e.Question, l.secrets)
	e.Error = Mask(e.Error, l.secrets)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.f.Write(append(b, '\n'))
	return err
}

// Close closes the log file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	return l.f.Close()
}

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`),                            // OpenAI and Anthropic keys
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_\w{20,})`), // GitHub tokens
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`),                     // Slack tokens
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                // AWS access key IDs
	regexp.MustCompile(`\beyJ[\w\-]{8,}\.[\w\-]{8,}\.[\w\-]{8,}`),             // JWTs
	regexp.MustCompile(`(?i)\b(bearer|token|password|passwd|secret|api[_-]?key)(\s*[:=]\s*|\s+)\S{6,}`),
	regexp.MustCompile(`\b[A-Za-z0-9+/_\-]{40,}={0,2}`), // long opaque tokens (session tokens, hashes)
	regexp.MustCompile(`\b\d{6}\b`),                     // could be a magic code
}

// Mask replaces exact secrets and secret-shaped substrings in s.
func Mask(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, redacted)
		}
	}
	for _, re := range patterns {
		s = re.ReplaceAllString(s, redacted)
	}
	return s
}
