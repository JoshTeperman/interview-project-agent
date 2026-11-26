# 002 tasks

- [x] Archive `001-auth` after M1 approval
- [x] `internal/snapshot`: `os.Root` jail, secret-name hiding, `project.json`, `list_files` / `search_files` / `read_file`, overview builder
- [x] Fixture snapshot `evaluation/fixtures/tiny-kv` and `evaluation/questions.txt`
- [x] `internal/llm`: `Provider` / `Conversation` interfaces; Anthropic Messages API (prompt caching) and OpenAI Responses API, both via `net/http` with shared retries
- [x] `prompts/system.md` (grounding, citation, and "not recorded" rules) and prompt version
- [x] `internal/agent`: tool loop, step cap, sources, step callback
- [x] `internal/questionlog`: masked JSON Lines log
- [x] `internal/chat` and `POST /api/chat`, `GET /api/starters`: limits, turn cap, one question at a time, logout drops the conversation
- [x] Config: `LLM_MAX_OUTPUT_TOKENS`, `LLM_TIMEOUT`, `TOOL_OUTPUT_MAX_CHARS`, `OVERVIEW_MAX_CHARS`; startup fails closed on a bad snapshot
- [x] `cmd/compare`: same questions against both providers, Markdown report
- [x] `Dockerfile` copies `go.sum` (empty now, kept so adding a dependency later just works)
- [x] Tests: traversal and symlink escapes, hidden secrets, tool behaviour, overview, step cap (1, 2, and 4 calls per reply), no shell tool, grounding rules in the prompt, provider request and response shapes, masking, chat error states
- [x] Specs: `docs/specs/agent.md`; config spec updated
- [ ] Live comparison run against both providers: needs `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` (dedicated keys with spend caps)
