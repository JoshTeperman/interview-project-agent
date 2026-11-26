# 002 results

Checked on the VPS on 2026-11-24 with Go 1.27.4 in a `golang:1.27` container (the host has no C compiler, which `-race` needs).

## Tests

`gofmt -l .` and `go vet ./...` are clean. `go test -race -count=1 ./...` passes.

| Package | Tests | Cases |
| --- | --- | --- |
| `snapshot` | 10 | `project.json` read; a malformed one fails, a missing one is fine. 19 hostile paths rejected by all three tools: `..` forms, absolute paths, `~`, backslashes, NUL, a symlink to a file outside, a symlink to the parent directory, `.git`, `.env`, `id_rsa`, `*.pem`. A whole-snapshot search finds none of the planted secrets (hidden, binary, or linked out); listings and the overview don't show them. A symlink that stays inside works. Listing, scoped case-insensitive search, numbered reads with ranges, a truncated read with a continuation note, directory and binary reads refused. The overview has the name, an indented tree, the session index with titles, and the overview documents; it is deterministic and respects its size budget. |
| `llm` | 7 | Against fake HTTP servers. Anthropic: `x-api-key` and `anthropic-version: 2026-10-21` headers, system block and top-level `cache_control: ephemeral`, tool schema, `tool_choice` auto then none, the second request replays the first messages byte-for-byte plus the assistant turn verbatim (thinking block and signature included) and the `tool_result`; usage mapping; refusal; an API error doesn't contain the key. OpenAI: `store: false`, `include: reasoning.encrypted_content`, function tool shape, `tool_choice`, `max_output_tokens`, the reasoning item (with `encrypted_content`) and function call passed back verbatim, `function_call_output` with `call_id`; cached-token usage; a 429 is retried twice, then the error doesn't contain the key and history is unchanged; `max_output_tokens` truncation flag; refusal. Retries: a 529 then a 503 then success returns the answer after 3 attempts; a 400 is not retried. |
| `agent` | 7 | A grounded turn: search, then a ranged read; the read file is listed as a source; usage summed. Step cap of 6 with 1, 2, and 4 tool calls per reply: exactly 6 run, extra calls get the cap message, the last call has tools disabled, at most 7 model calls. A model that ignores "no tools" gets the fallback answer, and its results stay queued. Eight escape attempts through the tools (including an unknown `run_shell` tool and bad JSON arguments) all fail with no secret in any result. Exactly three tools exist. The system prompt contains the citation, "not recorded", "never invent", and "data, not instructions" rules plus the overview, and no secrets. Provider errors are returned. |
| `questionlog` | 3 | Masking of OpenAI, Anthropic, GitHub, AWS, and bearer tokens, `password:` values, long opaque strings, 6-digit codes, and exact configured secrets; ordinary text (decision IDs, dates, 4-digit numbers) left alone. File mode 0600, one JSON line per question. A nil logger is a no-op. |
| `server` (chat) | 6 new | Signed-in chat returns the answer, sources, steps, and remaining turns; the question log entry is masked; the server log has `chat_turn` but neither the question nor the session token. `/api/starters` and `/api/chat` return 401 without a session, with no answer text. Empty, too long, turn cap (403), and per-session rate limit (429). Provider error (502), timeout (504), and refusal (422) without upstream detail in the body. A second question while one is running gets 409. Logout drops the conversation. |
| `config` | updated | Defaults for the four new settings. |

## Smoke run (real binary, fixture snapshot, dummy keys)

| Check | Result |
| --- | --- |
| Startup | `snapshot loaded`, project `tiny-kv`, system prompt 3862 characters, prompt version `3ae0d3e126b7`, 3 starter questions |
| `GET /api/starters`, `GET /api/me`, `POST /api/chat` without a cookie | 401 each |
| A grep for the dummy secret values in the log | 0 matches |
| Snapshot with a malformed `project.json` | refuses to start, exit code 2: `cannot open snapshot; refusing to start` |
| `cmd/compare` with no API keys | skips both providers and exits 1 with `no providers to run` |

## Build

Cold builds: modules already downloaded, empty build cache. "Container peak" is the build container's cgroup `memory.peak`. The image build was run with `--no-cache`, and host memory in use was sampled every second.

| Measure | With the Anthropic SDK (`d95e8b9`) | Hand-written clients, no dependencies |
| --- | --- | --- |
| `go build ./cmd/server`, peak memory in the container | 2481 MB and 2564 MB (two runs) | 383 MB (two runs, identical) |
| `go build ./cmd/server`, time | 151 s | 50 s (two runs) |
| Stripped binary | 12.6 MB | 7.6 MB |
| `docker build --no-cache`, host memory in use | peaked at 3752 of 3814 MB | 1468 MB before, 1678 MB peak |
| `docker build --no-cache`, time | not measured | 88 s |
| Image | 23.9 MB | 17.6 MB |

The official OpenAI Go SDK v3.106.0 couldn't be compiled here at all: the compiler was killed for lack of memory even building one package at a time, with host usage at 3737 MB.

## Not yet verified

- A live run of `cmd/compare` against `gpt-6-luna` and `claude-haiku-5-5`. No API keys are set on the VPS yet. Once dedicated, spend-capped keys are in the environment:

  ```sh
  go run ./cmd/compare -snapshot evaluation/fixtures/tiny-kv -questions evaluation/questions.txt -out compare.md
  ```

  That run also confirms that prompt caching works (cached input tokens above zero from the second model call) and that both real APIs accept the request shapes the tests check.
