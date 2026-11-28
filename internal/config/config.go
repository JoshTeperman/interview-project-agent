// Package config loads the app's settings from environment variables.
//
// Loading fails closed: any missing or invalid required value is an error,
// and the server must not start. Secret values are never included in errors
// or in String output.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultModel returns the model used for provider when LLM_MODEL is unset, or "".
func DefaultModel(provider string) string { return defaultModels[provider] }

// Provider names accepted in LLM_PROVIDER.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
)

// defaultModels maps each provider to the model used when LLM_MODEL is unset (S-D9).
var defaultModels = map[string]string{
	ProviderOpenAI:    "gpt-6-luna",
	ProviderAnthropic: "claude-haiku-5-5",
}

// Config holds every runtime setting. Secret fields are tagged in String.
type Config struct {
	ListenAddr string

	// Access control (S-D4).
	AllowedDomains []string // lower-case, without "@"
	AllowedEmails  []string // lower-case

	// LLM (S-D9).
	LLMProvider           string
	LLMModel              string
	OpenAIAPIKey          string
	AnthropicAPIKey       string
	LLMMaxOutputTokens    int           // per model call, including reasoning tokens
	LLMTimeout            time.Duration // per model call
	QuestionTimeout       time.Duration // whole question, all model calls and tools
	ContextMaxInputTokens int           // start a fresh conversation before input would exceed this

	// Email (S-D10).
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPFromName string

	// Snapshot (S-D11).
	SnapshotDir string

	// Local writable data (question log). Empty disables the file log.
	DataDir string

	// Limits (S-D2, S-D6).
	CodeTTL                  time.Duration
	SessionTTL               time.Duration
	CodeRequestsPerIPHour    int
	CodeRequestsPerEmailHour int
	VerifyAttemptsPerCode    int
	VerifyPerIPHour          int
	ChatPerSessionMinute     int
	ChatPerIPMinute          int
	MaxMessageChars          int
	SessionTurnCap           int
	ToolStepCap              int
	ToolOutputMaxChars       int // text one tool call may return to the model
	OverviewMaxChars         int // size of the snapshot overview in the system prompt

	// TrustForwardedFor makes the client IP come from the last X-Forwarded-For
	// entry. Enable it only when a trusted reverse proxy (Tailscale Funnel) sets it.
	TrustForwardedFor bool
}

// Load reads the configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads the configuration using getenv, which makes it testable.
// It returns every problem found, joined, so a deploy can fix them in one pass.
func LoadFrom(getenv func(string) string) (*Config, error) {
	l := loader{getenv: getenv}
	c := &Config{
		ListenAddr: l.str("LISTEN_ADDR", ":8080"),

		AllowedDomains: l.list("ALLOWED_EMAIL_DOMAINS", func(s string) string { return strings.TrimPrefix(s, "@") }),
		AllowedEmails:  l.list("ALLOWED_EMAILS", nil),

		LLMProvider:           strings.ToLower(l.str("LLM_PROVIDER", ProviderOpenAI)),
		LLMModel:              l.str("LLM_MODEL", ""),
		OpenAIAPIKey:          l.str("OPENAI_API_KEY", ""),
		AnthropicAPIKey:       l.str("ANTHROPIC_API_KEY", ""),
		LLMMaxOutputTokens:    l.int("LLM_MAX_OUTPUT_TOKENS", 16000),
		LLMTimeout:            l.duration("LLM_TIMEOUT", 120*time.Second),
		QuestionTimeout:       l.duration("QUESTION_TIMEOUT", 3*time.Minute),
		ContextMaxInputTokens: l.int("CONTEXT_MAX_INPUT_TOKENS", 100000),

		SMTPHost:     l.str("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:     l.int("SMTP_PORT", 587),
		SMTPUser:     l.required("SMTP_USER"),
		SMTPPass:     l.required("SMTP_PASS"),
		SMTPFromName: l.str("SMTP_FROM_NAME", "Josh Projects Guide"),

		SnapshotDir: l.required("SNAPSHOT_DIR"),
		DataDir:     l.str("DATA_DIR", ""),

		CodeTTL:                  l.duration("CODE_TTL", 10*time.Minute),
		SessionTTL:               l.duration("SESSION_TTL", 12*time.Hour),
		CodeRequestsPerIPHour:    l.int("CODE_REQUESTS_PER_IP_HOUR", 10),
		CodeRequestsPerEmailHour: l.int("CODE_REQUESTS_PER_EMAIL_HOUR", 3),
		VerifyAttemptsPerCode:    l.int("VERIFY_ATTEMPTS_PER_CODE", 5),
		VerifyPerIPHour:          l.int("VERIFY_PER_IP_HOUR", 30),
		ChatPerSessionMinute:     l.int("CHAT_PER_SESSION_MINUTE", 6),
		ChatPerIPMinute:          l.int("CHAT_PER_IP_MINUTE", 12),
		MaxMessageChars:          l.int("MAX_MESSAGE_CHARS", 2000),
		SessionTurnCap:           l.int("SESSION_TURN_CAP", 40),
		ToolStepCap:              l.int("TOOL_STEP_CAP", 6),
		ToolOutputMaxChars:       l.int("TOOL_OUTPUT_MAX_CHARS", 20000),
		OverviewMaxChars:         l.int("OVERVIEW_MAX_CHARS", 60000),

		TrustForwardedFor: l.bool("TRUST_FORWARDED_FOR", false),
	}

	if len(c.AllowedDomains) == 0 && len(c.AllowedEmails) == 0 {
		l.fail("ALLOWED_EMAIL_DOMAINS or ALLOWED_EMAILS must list at least one entry")
	}

	if def, ok := defaultModels[c.LLMProvider]; !ok {
		l.fail(fmt.Sprintf("LLM_PROVIDER must be %q or %q", ProviderOpenAI, ProviderAnthropic))
	} else {
		if c.LLMModel == "" {
			c.LLMModel = def
		}
		if c.APIKey() == "" {
			l.fail(fmt.Sprintf("%s is required when LLM_PROVIDER=%s", apiKeyVar(c.LLMProvider), c.LLMProvider))
		}
	}

	if c.SnapshotDir != "" {
		if fi, err := os.Stat(c.SnapshotDir); err != nil || !fi.IsDir() {
			l.fail("SNAPSHOT_DIR must be an existing directory")
		}
	}

	for name, v := range map[string]int{
		"SMTP_PORT": c.SMTPPort, "CODE_REQUESTS_PER_IP_HOUR": c.CodeRequestsPerIPHour,
		"CODE_REQUESTS_PER_EMAIL_HOUR": c.CodeRequestsPerEmailHour, "VERIFY_ATTEMPTS_PER_CODE": c.VerifyAttemptsPerCode,
		"VERIFY_PER_IP_HOUR":      c.VerifyPerIPHour,
		"CHAT_PER_SESSION_MINUTE": c.ChatPerSessionMinute, "CHAT_PER_IP_MINUTE": c.ChatPerIPMinute,
		"MAX_MESSAGE_CHARS": c.MaxMessageChars, "SESSION_TURN_CAP": c.SessionTurnCap, "TOOL_STEP_CAP": c.ToolStepCap,
		"LLM_MAX_OUTPUT_TOKENS": c.LLMMaxOutputTokens, "TOOL_OUTPUT_MAX_CHARS": c.ToolOutputMaxChars,
		"OVERVIEW_MAX_CHARS": c.OverviewMaxChars, "CONTEXT_MAX_INPUT_TOKENS": c.ContextMaxInputTokens,
	} {
		if v <= 0 {
			l.fail(name + " must be a positive integer")
		}
	}

	if c.ToolOutputMaxChars > 0 && c.ToolOutputMaxChars < 2000 {
		l.fail("TOOL_OUTPUT_MAX_CHARS must be at least 2000")
	}

	if err := errors.Join(l.errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// APIKey returns the API key for the configured provider.
func (c *Config) APIKey() string {
	if c.LLMProvider == ProviderAnthropic {
		return c.AnthropicAPIKey
	}
	return c.OpenAIAPIKey
}

// EmailAllowed reports whether email is on the explicit allowlist or in an allowed domain.
// email must already be trimmed and lower-cased.
func (c *Config) EmailAllowed(email string) bool {
	for _, e := range c.AllowedEmails {
		if e == email {
			return true
		}
	}
	at := strings.LastIndexByte(email, '@')
	if at < 1 || at == len(email)-1 {
		return false
	}
	domain := email[at+1:]
	for _, d := range c.AllowedDomains {
		if d == domain {
			return true
		}
	}
	return false
}

// String renders the config for startup logs, with secrets masked.
func (c *Config) String() string {
	return fmt.Sprintf("listen=%s provider=%s model=%s api_key=%s smtp=%s:%d smtp_user=%s smtp_pass=%s snapshot=%s domains=%d emails=%d step_cap=%d turn_cap=%d",
		c.ListenAddr, c.LLMProvider, c.LLMModel, Mask(c.APIKey()), c.SMTPHost, c.SMTPPort,
		c.SMTPUser, Mask(c.SMTPPass), c.SnapshotDir, len(c.AllowedDomains), len(c.AllowedEmails),
		c.ToolStepCap, c.SessionTurnCap)
}

// Mask hides a secret, saying only whether it is set.
func Mask(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<set>"
}

func apiKeyVar(provider string) string {
	if provider == ProviderAnthropic {
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

type loader struct {
	getenv func(string) string
	errs   []error
}

func (l *loader) fail(msg string) { l.errs = append(l.errs, errors.New(msg)) }

func (l *loader) str(name, def string) string {
	if v := strings.TrimSpace(l.getenv(name)); v != "" {
		return v
	}
	return def
}

func (l *loader) required(name string) string {
	v := strings.TrimSpace(l.getenv(name))
	if v == "" {
		l.fail(name + " is required")
	}
	return v
}

func (l *loader) int(name string, def int) int {
	v := strings.TrimSpace(l.getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail(name + " must be an integer")
	}
	return n
}

func (l *loader) bool(name string, def bool) bool {
	v := strings.TrimSpace(l.getenv(name))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(name + " must be true or false")
	}
	return b
}

func (l *loader) duration(name string, def time.Duration) time.Duration {
	v := strings.TrimSpace(l.getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.fail(name + " must be a positive duration such as 10m")
	}
	return d
}

// list splits a comma-separated variable into trimmed, lower-cased, non-empty entries.
func (l *loader) list(name string, norm func(string) string) []string {
	var out []string
	for _, p := range strings.Split(l.getenv(name), ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if norm != nil {
			p = norm(p)
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
