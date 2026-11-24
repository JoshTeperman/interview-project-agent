package questionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMask(t *testing.T) {
	cases := map[string]string{
		"my key is sk-proj-abcdefghijklmnop1234 ok":        "my key is [redacted] ok",
		"anthropic sk-ant-api03-ABCDEFGHIJKLMNOPqrst":      "anthropic [redacted]",
		"code 123456 please":                               "code [redacted] please",
		"use ghp_ABCDEFGHIJKLMNOPQRSTuvwx now":             "use [redacted] now",
		"password: hunter2hunter2":                         "[redacted]",
		"Authorization: Bearer abcdefghijklmnop":           "Authorization: [redacted]",
		"session Zm9vYmFyYmF6cXV4Zm9vYmFyYmF6cXV4Zm9vYmFy": "session [redacted]",
		"AKIAABCDEFGHIJKLMNOP leaked":                      "[redacted] leaked",
		"why was the 4x ratio chosen in KV-D3?":            "why was the 4x ratio chosen in KV-D3?",
		"what about line 1234 or 2026-10-03?":              "what about line 1234 or 2026-10-03?",
	}
	for in, want := range cases {
		if got := Mask(in, nil); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Mask("the smtp pass is CorrectHorse!", []string{"CorrectHorse!"}); got != "the smtp pass is [redacted]" {
		t.Errorf("exact secret: %q", got)
	}
}

func TestLogWritesMaskedJSONLines(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "s3cr3t-value")
	if err != nil {
		t.Fatal(err)
	}
	l.Log(Entry{Time: time.Unix(0, 0).UTC(), Email: "a@example.com", Question: "is s3cr3t-value or 654321 in the repo?", Steps: 2})
	l.Log(Entry{Question: "second", Error: "failed with sk-abcdefghijklmnopqrstu"})
	l.Close()

	fi, _ := os.Stat(filepath.Join(dir, FileName))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	var e Entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Question != "is [redacted] or [redacted] in the repo?" || e.Email != "a@example.com" || e.Steps != 2 {
		t.Fatalf("entry = %+v", e)
	}
	if strings.Contains(string(b), "s3cr3t") || strings.Contains(string(b), "654321") || strings.Contains(string(b), "sk-abc") {
		t.Fatalf("secret in log: %s", b)
	}
}

func TestNilLoggerIsANoop(t *testing.T) {
	l, err := Open("")
	if err != nil || l != nil {
		t.Fatalf("l=%v err=%v", l, err)
	}
	if err := l.Log(Entry{Question: "q"}); err != nil {
		t.Fatal(err)
	}
}
