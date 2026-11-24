package config

import (
	"strings"
	"testing"
)

func env(t *testing.T, over map[string]string) func(string) string {
	t.Helper()
	base := map[string]string{
		"ALLOWED_EMAIL_DOMAINS": "@Example.com, other.org",
		"OPENAI_API_KEY":        "sk-test-secret",
		"SMTP_USER":             "agent@example.com",
		"SMTP_PASS":             "app-password-secret",
		"SNAPSHOT_DIR":          t.TempDir(),
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := LoadFrom(env(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMProvider != "openai" || c.LLMModel != "gpt-6-luna" {
		t.Errorf("provider/model = %s/%s", c.LLMProvider, c.LLMModel)
	}
	if c.SMTPHost != "smtp.gmail.com" || c.SMTPPort != 587 || c.ToolStepCap != 6 {
		t.Errorf("defaults wrong: %+v", c)
	}
	if got := strings.Join(c.AllowedDomains, ","); got != "example.com,other.org" {
		t.Errorf("domains = %q", got)
	}
}

func TestAnthropicDefaultModel(t *testing.T) {
	c, err := LoadFrom(env(t, map[string]string{"LLM_PROVIDER": "anthropic", "ANTHROPIC_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMModel != "claude-haiku-5-5" || c.APIKey() != "k" {
		t.Errorf("model=%s", c.LLMModel)
	}
}

func TestFailsClosed(t *testing.T) {
	cases := map[string]map[string]string{
		"empty env":          {"ALLOWED_EMAIL_DOMAINS": "", "OPENAI_API_KEY": "", "SMTP_USER": "", "SMTP_PASS": "", "SNAPSHOT_DIR": ""},
		"no allowlist":       {"ALLOWED_EMAIL_DOMAINS": " , "},
		"missing key":        {"OPENAI_API_KEY": ""},
		"key for wrong prov": {"LLM_PROVIDER": "anthropic"},
		"bad provider":       {"LLM_PROVIDER": "xai"},
		"missing smtp pass":  {"SMTP_PASS": ""},
		"snapshot missing":   {"SNAPSHOT_DIR": "/nonexistent/snapshot"},
		"bad int":            {"TOOL_STEP_CAP": "six"},
		"zero cap":           {"TOOL_STEP_CAP": "0"},
		"bad duration":       {"CODE_TTL": "-1m"},
	}
	for name, over := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFrom(env(t, over)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestErrorsAndStringHideSecrets(t *testing.T) {
	_, err := LoadFrom(env(t, map[string]string{"SNAPSHOT_DIR": ""}))
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
	c, err := LoadFrom(env(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if s := c.String(); strings.Contains(s, "secret") || !strings.Contains(s, "api_key=<set>") {
		t.Errorf("String leaked or masked wrong: %s", s)
	}
}

func TestEmailAllowed(t *testing.T) {
	c, err := LoadFrom(env(t, map[string]string{"ALLOWED_EMAILS": "Friend@Gmail.com"}))
	if err != nil {
		t.Fatal(err)
	}
	for email, want := range map[string]bool{
		"a@example.com":         true,
		"friend@gmail.com":      true,
		"other@gmail.com":       false,
		"a@sub.example.com":     false,
		"a@example.com.evil.io": false,
		"example.com":           false,
		"@example.com":          false,
		"a@":                    false,
	} {
		if got := c.EmailAllowed(email); got != want {
			t.Errorf("EmailAllowed(%q) = %v, want %v", email, got, want)
		}
	}
}
