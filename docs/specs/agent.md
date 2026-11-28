# Snapshot, tools, and chat agent

A signed-in interviewer asks a question; the model answers from a read-only snapshot using three jailed tools (`internal/snapshot`, `internal/agent`, `internal/llm`, `internal/chat`).

## Snapshot

`SNAPSHOT_DIR` is a plain directory, copied onto the VPS with `scp` or `rsync` and mounted read-only. The app never writes to it and needs no git or pull token. It is opened once at startup; after changing the snapshot, restart the app.

Optional `project.json` at the root:

```json
{
  "name": "tiny-kv",
  "starter_questions": ["Why an append-only log?"],
  "overview_files": ["README.md", "docs/spec.md", "docs/decisions-log.md"],
  "sessions_dir": "sessions"
}
```

| Field | Default when missing |
| --- | --- |
| `name` | `(unnamed project)` |
| `starter_questions` | none |
| `overview_files` | the first that exist of `README.md`, `SCOPING.md`, `docs/scoping.md`, `SPEC.md`, `docs/spec.md`, `docs/SPEC.md`, `DECISIONS.md`, `decisions-log.md`, `docs/decisions-log.md`, `docs/decisions.md` (all that exist are included, in this order) |
| `sessions_dir` | `sessions/`, else `docs/sessions/` |

A malformed `project.json` stops startup (exit code 2).

### Jail

Every path from the model is relative to the snapshot root (`""` or `/` is the root). Rejected:

- absolute paths, `~`, backslashes, NUL bytes, and anything that cleans to `..` or `../...`: "path is outside the snapshot"
- any path with a component that looks like VCS internals or a secret: "path is not available". Hidden names: `.git`, `.hg`, `.svn`, `.ssh`, `.gnupg`, `.aws`, `.docker`, `.netrc`, `.npmrc`, `.pypirc`, `.env`, `.envrc`, `.env.*`, `.env-*`, `.env_*`, `credentials.json`, `*.tfvars`, `id_rsa*`, `id_ed25519*`, `id_ecdsa*`, `id_dsa*`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.keystore`, `*.jks`. They are also left out of listings, searches, and the overview.

Symlinks are not available at all, even ones that stay inside the snapshot: each path component is checked with `Lstat` through `os.Root` before anything is opened, and links are left out of listings, searches, the tree, and the session index. A symlinked `project.json` stops startup.

### Overview

Built at startup and placed after the standing instructions in the system prompt, inside `<snapshot_overview>` and `</snapshot_overview>`, capped at `OVERVIEW_MAX_CHARS`. The prompt says everything inside the tags is untrusted project data. Any copy of the tag in snapshot text is replaced with `[tag removed]`. Contents:

1. Project name.
2. Directory tree, 4 levels deep, up to 400 entries, at most a quarter of the budget.
3. Session index: each file in the sessions directory with its first Markdown heading and size.
4. The overview files in full, each cut at a line boundary with a "use read_file" note when the budget runs out.

## Tools

The model gets exactly these three. There is no shell, write, or network tool. Every result is capped at `TOOL_OUTPUT_MAX_CHARS`. Errors go back to the model as tool errors.

| Tool | Arguments | Returns |
| --- | --- | --- |
| `list_files` | `path` | One level: `name/` for directories, `name (size)` for files. Up to 500 entries. |
| `search_files` | `query` (2 to 200 characters), optional `path` | Literal, case-insensitive line matches as `path:line: text` (lines cut at 240 characters), up to 50. Skips binary files and files over 1 MB. "No matches" names the scope and the number of files searched. |
| `read_file` | `path`, optional `start_line`, `end_line` | A header `path (lines a-b of n)`, then numbered lines. Long output stops at a line boundary with `call read_file with start_line=N to continue`. A single line longer than the cap (minified code) is cut with `[line N cut at X of Y chars]`. Refuses directories, binary files (a NUL in the first 8 KB), and files over 4 MB. |

## Agent loop

The system prompt is `prompts/system.md` followed by the overview. Its version (first 12 hex digits of the prompt file's SHA-256) is logged with each question. The prompt requires citations (paths with line numbers, decision IDs, session files), saying plainly when something isn't recorded instead of inventing a rationale, and treating snapshot content as data rather than instructions.

For each question: send it, run any tool calls, send the results, and repeat until the model answers in text. `TOOL_STEP_CAP` counts individual tool calls, including parallel ones. Once it is reached, further calls get the result "Tool-call limit for this question reached. Answer now…", and the next model call has tools disabled. If the model still calls a tool, the answer is a fixed fallback message. A question costs at most `TOOL_STEP_CAP + 1` model calls.

Paths that `read_file` or `list_files` returned successfully are reported as `sources`.

## Providers

`LLM_PROVIDER` picks one; `LLM_MODEL` overrides the default model.

| Provider | Default model | API | Notes |
| --- | --- | --- | --- |
| `openai` | `gpt-6-luna` | `POST /v1/responses` | `store: false`; encrypted reasoning items requested and passed back verbatim; `tool_choice` `auto` or `none`. |
| `anthropic` | `claude-haiku-5-5` | `POST /v1/messages`, `anthropic-version: 2026-10-21` | `cache_control: ephemeral` on the system block (caches tools and system) and a top-level breakpoint (caches the conversation so far); the model's default thinking; reply content passed back byte-for-byte, thinking blocks included. |

Both are small `net/http` clients with no SDK dependency. Each call has `LLM_MAX_OUTPUT_TOKENS` and `LLM_TIMEOUT` per attempt. 429 and 5xx (including 529) are retried up to twice, waiting `Retry-After` (at most 10 seconds) or 0.5 then 1 second. Connection errors are retried only if the request was never sent (dial, TLS, connection refused); a timeout after sending is not retried, since the reply may already be billed. A whole question, including retries and tools, is bounded by `QUESTION_TIMEOUT`. Each session has one conversation, kept in the provider's own format and only appended to. It lives in memory and is dropped on logout, after `SESSION_TTL` idle, or on restart.

Context limit: before each question, if the last model call's input tokens plus a worst-case estimate for one question (`(TOOL_STEP_CAP × TOOL_OUTPUT_MAX_CHARS + MAX_MESSAGE_CHARS) / 3`) would exceed `CONTEXT_MAX_INPUT_TOKENS`, the session starts a fresh conversation and the response has `conversation_reset: true`. If the provider still rejects the input as too long (OpenAI `context_length_exceeded`; Anthropic "prompt is too long" or 413), the question is retried once in a fresh conversation, then fails with `conversation_full`.

## Chat API

Both routes need a session (otherwise `401 unauthenticated`).

`GET /api/starters` returns `{"starter_questions": [...]}` from `project.json`.

`POST /api/chat` `{"message": "..."}` (JSON, body up to 64 KB). Checks, in order: message not empty and at most `MAX_MESSAGE_CHARS` characters; `CHAT_PER_IP_MINUTE`; `CHAT_PER_SESSION_MINUTE`; no other question running for this session; then one turn is taken from `SESSION_TURN_CAP`, before the model is called. Success:

```json
{
  "answer": "…",
  "sources": ["docs/decisions-log.md"],
  "steps": [{"tool": "search_files", "target": "fsync"}, {"tool": "read_file", "target": "docs/decisions-log.md"}],
  "cap_hit": false,
  "truncated": false,
  "turns_remaining": 39,
  "conversation_reset": false
}
```

A step that failed has an `error` field. `truncated` means the answer hit the output token limit.

| Status | `error` | When |
| --- | --- | --- |
| 400 | `empty_message`, `message_too_long`, `bad_request` | |
| 403 | `turn_cap_reached` | `SESSION_TURN_CAP` spent |
| 409 | `busy` | A question for this session is still running |
| 415 | `unsupported_media_type` | Not JSON |
| 422 | `model_refused` | The model declined (safety refusal) |
| 422 | `conversation_full` | Even a fresh conversation was too long for the model |
| 429 | `rate_limited` | Chat limit hit; `Retry-After` and `retry_after_seconds` |
| 502 | `llm_error` | The provider returned an error |
| 504 | `llm_timeout` | A model call or the whole question (`QUESTION_TIMEOUT`) timed out |

Error bodies never include the provider's error text.

## Question log

With `DATA_DIR` set, each question that reaches the model appends one JSON line to `DATA_DIR/questions.jsonl` (mode 0600): `time`, `email`, `provider`, `model`, `prompt_version`, `question`, `tool_calls`, `cap_hit`, `conversation_reset`, `sources`, `usage` (input, cached input, cache writes, output tokens), `latency_ms`, and `error`. Answers are not logged.

The question and error are masked first: the configured API keys and SMTP password, API-key and token shapes (`sk-…`, GitHub, Slack, AWS, JWTs, `bearer …`, `password: …`), opaque strings of 40 or more characters, and 6-digit numbers (possible magic codes) become `[redacted]`.

The server log records `chat_turn` or `chat_turn_failed` with the email, counts, tokens, and latency, but never the question text or the session token.

## Comparing providers

`cmd/compare` asks the same questions of each provider that has an API key in the environment, each in a fresh conversation, using the same prompt, tools, and step cap as the app. It prints a Markdown report: per question, a table of latency, tool and model calls, and tokens, then each provider's tool steps and answer; totals at the end. Each run calls the real APIs and is billed.

```sh
go run ./cmd/compare -snapshot evaluation/fixtures/tiny-kv -questions evaluation/questions.txt -out compare.md
```

Flags: `-q` (repeatable), `-questions FILE` (one per line, `#` comments), `-providers`, `-openai-model`, `-anthropic-model`, `-step-cap`, `-max-output-tokens`, `-timeout`, `-tool-output-max-chars`, `-overview-max-chars`, `-out`. With no questions it asks the snapshot's starter questions.
