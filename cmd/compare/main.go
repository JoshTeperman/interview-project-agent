// Command compare runs the same questions against several LLM providers over one
// snapshot and prints a side-by-side Markdown report (S-D9).
//
// It uses the same agent loop, prompt, tools, and step cap as the web app. Each
// question starts a fresh conversation per provider. API keys come from
// OPENAI_API_KEY and ANTHROPIC_API_KEY; a provider without a key is skipped.
//
//	go run ./cmd/compare -snapshot ./evaluation/fixtures/tiny-kv -q "Why an append-only log?"
//	go run ./cmd/compare -snapshot /srv/snapshot -questions questions.txt -out report.md
//
// With no -q or -questions it asks the snapshot's starter questions.
// Every run calls the real APIs and is billed.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JoshTeperman/interview-project-agent/internal/agent"
	"github.com/JoshTeperman/interview-project-agent/internal/config"
	"github.com/JoshTeperman/interview-project-agent/internal/llm"
	"github.com/JoshTeperman/interview-project-agent/internal/snapshot"
	"github.com/JoshTeperman/interview-project-agent/prompts"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, "; ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

type result struct {
	provider, model string
	turn            agent.Turn
	latency         time.Duration
	err             error
}

func main() {
	var qs multi
	snapDir := flag.String("snapshot", os.Getenv("SNAPSHOT_DIR"), "snapshot directory (default $SNAPSHOT_DIR)")
	qfile := flag.String("questions", "", "file with one question per line (# starts a comment)")
	flag.Var(&qs, "q", "a question (repeatable)")
	providers := flag.String("providers", "openai,anthropic", "comma-separated providers to compare")
	openaiModel := flag.String("openai-model", config.DefaultModel(llm.OpenAI), "OpenAI model")
	anthropicModel := flag.String("anthropic-model", config.DefaultModel(llm.Anthropic), "Anthropic model")
	stepCap := flag.Int("step-cap", 6, "tool calls per question")
	maxOut := flag.Int64("max-output-tokens", 16000, "output token limit per model call")
	timeout := flag.Duration("timeout", 120*time.Second, "timeout per model call")
	toolChars := flag.Int("tool-output-max-chars", 20000, "text one tool call may return")
	overviewChars := flag.Int("overview-max-chars", 60000, "size of the snapshot overview")
	out := flag.String("out", "", "write the report here instead of stdout")
	flag.Parse()

	if *snapDir == "" {
		die("set -snapshot or SNAPSHOT_DIR")
	}
	snap, err := snapshot.Open(*snapDir, snapshot.Limits{MaxToolChars: *toolChars, MaxOverviewChars: *overviewChars})
	if err != nil {
		die(err.Error())
	}
	defer snap.Close()

	questions := []string(qs)
	if *qfile != "" {
		fq, err := readQuestions(*qfile)
		if err != nil {
			die(err.Error())
		}
		questions = append(questions, fq...)
	}
	if len(questions) == 0 {
		questions = snap.Project.StarterQuestions
	}
	if len(questions) == 0 {
		die("no questions: pass -q or -questions, or add starter_questions to project.json")
	}

	models := map[string]string{llm.OpenAI: *openaiModel, llm.Anthropic: *anthropicModel}
	keys := map[string]string{llm.OpenAI: os.Getenv("OPENAI_API_KEY"), llm.Anthropic: os.Getenv("ANTHROPIC_API_KEY")}
	var provs []llm.Provider
	for _, name := range strings.Split(*providers, ",") {
		name = strings.TrimSpace(name)
		if keys[name] == "" {
			fmt.Fprintf(os.Stderr, "skipping %s: no API key in the environment\n", name)
			continue
		}
		p, err := llm.New(llm.Options{Provider: name, Model: models[name], APIKey: keys[name], MaxOutputTokens: *maxOut, Timeout: *timeout})
		if err != nil {
			die(err.Error())
		}
		provs = append(provs, p)
	}
	if len(provs) == 0 {
		die("no providers to run")
	}

	ag := agent.New(snap, *stepCap)
	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			die(err.Error())
		}
		defer f.Close()
		w = f
	}

	fmt.Fprintf(w, "# Provider comparison\n\n")
	fmt.Fprintf(w, "Snapshot: %s. Prompt version: `%s`. Step cap: %d. Run: %s.\n\n",
		nameOr(snap.Project.Name), prompts.Version(), *stepCap, time.Now().UTC().Format(time.RFC3339))
	totals := map[string]*llm.Usage{}
	for _, p := range provs {
		totals[p.Name()] = &llm.Usage{}
	}

	for i, q := range questions {
		fmt.Fprintf(os.Stderr, "question %d/%d\n", i+1, len(questions))
		results := make([]result, len(provs))
		var wg sync.WaitGroup
		for j, p := range provs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				turn, err := ag.Ask(context.Background(), ag.NewConversation(p), q, nil)
				results[j] = result{provider: p.Name(), model: p.Model(), turn: turn, latency: time.Since(start), err: err}
			}()
		}
		wg.Wait()

		fmt.Fprintf(w, "## Q%d. %s\n\n", i+1, q)
		fmt.Fprintf(w, "| Provider | Model | Latency | Tool calls | LLM calls | Input (cached) | Output | Cap hit |\n|---|---|---|---|---|---|---|---|\n")
		for _, r := range results {
			u := r.turn.Usage
			fmt.Fprintf(w, "| %s | `%s` | %.1fs | %d | %d | %d (%d) | %d | %v |\n",
				r.provider, r.model, r.latency.Seconds(), len(r.turn.Steps), r.turn.LLMCalls, u.InputTokens, u.CachedInputTokens, u.OutputTokens, r.turn.CapHit)
			totals[r.provider].Add(u)
		}
		fmt.Fprintln(w)
		for _, r := range results {
			fmt.Fprintf(w, "### %s\n\n", r.provider)
			if r.err != nil {
				fmt.Fprintf(w, "**Error:** %s\n\n", r.err)
				continue
			}
			if len(r.turn.Steps) > 0 {
				var steps []string
				for _, s := range r.turn.Steps {
					st := fmt.Sprintf("`%s %s`", s.Tool, s.Target)
					if s.Error != "" {
						st += " (error)"
					}
					steps = append(steps, st)
				}
				fmt.Fprintf(w, "Tools: %s\n\n", strings.Join(steps, ", "))
			}
			fmt.Fprintf(w, "%s\n\n", r.turn.Answer)
		}
	}

	fmt.Fprintf(w, "## Totals\n\n| Provider | Input | Cached input | Cache writes | Output |\n|---|---|---|---|---|\n")
	for _, p := range provs {
		u := totals[p.Name()]
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d |\n", p.Name(), u.InputTokens, u.CachedInputTokens, u.CacheWriteTokens, u.OutputTokens)
	}
}

func readQuestions(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var qs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			qs = append(qs, line)
		}
	}
	return qs, sc.Err()
}

func nameOr(n string) string {
	if n == "" {
		return "(unnamed)"
	}
	return n
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "compare:", msg)
	os.Exit(1)
}
