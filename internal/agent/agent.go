// Package agent runs the question-answering tool loop (S-D2, R5, R6).
//
// For each question it sends the thread to the provider, runs any read-only tool
// calls against the snapshot, feeds the results back, and repeats until the model
// answers in text. Tool calls per question are capped: once the cap is reached,
// further calls are refused and the model is asked to answer without tools.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/JoshTeperman/interview-project-agent/internal/llm"
	"github.com/JoshTeperman/interview-project-agent/internal/snapshot"
	"github.com/JoshTeperman/interview-project-agent/prompts"
)

// Tool names. These three are the only tools the model ever gets (R5).
const (
	ToolList   = "list_files"
	ToolSearch = "search_files"
	ToolRead   = "read_file"
)

// CapReachedMessage is the tool result for calls past the step cap.
const CapReachedMessage = "Tool-call limit for this question reached. Answer now from what you have, and say what you couldn't check."

// FallbackAnswer is returned if the model still won't answer in text after the cap.
const FallbackAnswer = "I ran out of lookups for this question before I could finish. Try asking something narrower."

// Agent answers questions about one snapshot.
type Agent struct {
	snap    *snapshot.Snapshot
	system  string
	stepCap int
}

// New builds an Agent. The system prompt is the standing instructions followed
// by the snapshot overview inside <snapshot_overview> tags, which the prompt
// marks as untrusted data. It is fixed for the Agent's life so it caches.
func New(snap *snapshot.Snapshot, stepCap int) *Agent {
	tag := snapshot.OverviewTag
	return &Agent{
		snap:    snap,
		system:  prompts.System() + "\n\n<" + tag + ">\n" + snap.Overview() + "</" + tag + ">\n",
		stepCap: stepCap,
	}
}

// System returns the full system prompt.
func (a *Agent) System() string { return a.system }

// Tools returns the tool definitions given to the model.
func Tools() []llm.ToolDef {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	return []llm.ToolDef{
		{
			Name:        ToolList,
			Description: "List the files and directories at a path in the project snapshot. Directories end in '/'.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"path": str("Directory relative to the snapshot root. Use \"\" or \"/\" for the root."),
			}, "required": []string{"path"}},
		},
		{
			Name:        ToolSearch,
			Description: "Search text files in the project snapshot for a literal string (case-insensitive). Returns 'path:line: text' for each match.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"query": str("Literal text to find, 2 to 200 characters."),
				"path":  str("Optional directory or file to search in, relative to the snapshot root. Empty searches everything."),
			}, "required": []string{"query"}},
		},
		{
			Name:        ToolRead,
			Description: "Read a text file from the project snapshot. Lines are numbered for citation. Long files are cut off with a note saying how to continue.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"path":       str("File path relative to the snapshot root."),
				"start_line": integer("Optional first line to return (1-based)."),
				"end_line":   integer("Optional last line to return (inclusive)."),
			}, "required": []string{"path"}},
		},
	}
}

// NewConversation starts a thread for one interviewer session.
func (a *Agent) NewConversation(p llm.Provider) llm.Conversation {
	return p.NewConversation(a.system, Tools())
}

// Step is one tool call the model made.
type Step struct {
	Tool   string `json:"tool"`
	Target string `json:"target"` // the path or query, for display
	Error  string `json:"error,omitempty"`
}

// Turn is the outcome of one question.
type Turn struct {
	Answer    string    `json:"answer"`
	Steps     []Step    `json:"steps"`
	Sources   []string  `json:"sources"` // snapshot paths read or listed successfully
	Usage     llm.Usage `json:"usage"`
	LLMCalls  int       `json:"llm_calls"`
	CapHit    bool      `json:"cap_hit"`
	Truncated bool      `json:"truncated"` // the answer hit the output token limit
	// LastInputTokens is the input size of the turn's last model call: roughly
	// how much of the context window the conversation now uses.
	LastInputTokens int64 `json:"last_input_tokens"`
}

// Ask answers question in conv. onStep, if not nil, is called after each tool
// call, so a caller can show progress. On error, Turn holds what happened so far.
func (a *Agent) Ask(ctx context.Context, conv llm.Conversation, question string, onStep func(Step)) (Turn, error) {
	var t Turn
	sources := map[string]bool{}
	steps := 0
	allowTools := a.stepCap > 0
	text := question
	for {
		reply, err := conv.Send(ctx, text, allowTools)
		t.LLMCalls++
		t.Usage.Add(reply.Usage)
		if reply.Usage.InputTokens > 0 {
			t.LastInputTokens = reply.Usage.InputTokens
		}
		if err != nil {
			t.Sources = sorted(sources)
			return t, err
		}
		text = ""
		if len(reply.ToolCalls) == 0 {
			t.Answer = strings.TrimSpace(reply.Text)
			t.Truncated = reply.Truncated
			break
		}

		results := make([]llm.ToolResult, 0, len(reply.ToolCalls))
		for _, call := range reply.ToolCalls {
			if steps >= a.stepCap {
				t.CapHit = true
				results = append(results, llm.ToolResult{CallID: call.ID, Content: CapReachedMessage, IsError: true})
				continue
			}
			steps++
			out, step, src, err := a.run(call)
			if err != nil {
				step.Error = err.Error()
				results = append(results, llm.ToolResult{CallID: call.ID, Content: err.Error(), IsError: true})
			} else {
				results = append(results, llm.ToolResult{CallID: call.ID, Content: out})
				if src != "" {
					sources[src] = true
				}
			}
			t.Steps = append(t.Steps, step)
			if onStep != nil {
				onStep(step)
			}
		}
		conv.AddToolResults(results)

		if !allowTools {
			// The model was told not to call tools and did anyway. The results stay
			// queued for the next question, so the thread remains valid.
			t.Answer = FallbackAnswer
			break
		}
		if steps >= a.stepCap {
			allowTools = false
			t.CapHit = true
		}
	}
	t.Sources = sorted(sources)
	if t.Answer == "" {
		t.Answer = FallbackAnswer
	}
	return t, nil
}

type toolArgs struct {
	Path      string `json:"path"`
	Query     string `json:"query"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// run executes one tool call against the snapshot. It returns the output, a
// display step, and the snapshot path it touched (for Sources).
func (a *Agent) run(call llm.ToolCall) (out string, step Step, source string, err error) {
	step = Step{Tool: call.Name}
	var args toolArgs
	if len(call.Args) > 0 {
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return "", step, "", errors.New("arguments are not valid JSON for this tool")
		}
	}
	switch call.Name {
	case ToolList:
		step.Target = displayTarget(args.Path)
		out, err = a.snap.List(args.Path)
	case ToolSearch:
		step.Target = args.Query
		if args.Path != "" {
			step.Target = fmt.Sprintf("%s in %s", args.Query, args.Path)
		}
		out, err = a.snap.Search(args.Query, args.Path)
		return out, step, "", err // a search doesn't cite one file
	case ToolRead:
		step.Target = args.Path
		out, err = a.snap.Read(args.Path, args.StartLine, args.EndLine)
	default:
		return "", step, "", fmt.Errorf("unknown tool %q; the available tools are %s, %s, and %s", call.Name, ToolList, ToolSearch, ToolRead)
	}
	if err != nil {
		return "", step, "", err
	}
	return out, step, cleanSource(args.Path), nil
}

func displayTarget(p string) string {
	if strings.Trim(p, "/. ") == "" {
		return "/"
	}
	return p
}

func cleanSource(p string) string {
	p = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "./"))
	if p == "" || p == "/" || p == "." {
		return "/"
	}
	return strings.TrimSuffix(p, "/")
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
