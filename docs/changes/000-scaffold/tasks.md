# 000 tasks

- [x] Import scoping doc and decisions log from Notion into `docs/`
- [x] `go.mod`, `cmd/server`, `internal/config`, docs tree (`changes/`, `changes/archive/`, `specs/`)
- [x] Config covers allowed domains/emails, `LLM_PROVIDER`, `LLM_MODEL`, provider keys, `SMTP_*`, snapshot dir, rate limits, turn cap, step cap
- [x] Fail closed on missing or invalid values; secrets masked in logs and errors
- [x] Non-root Dockerfile; Compose with read-only snapshot mount and memory cap
- [x] `.env.example` with no secrets; `.gitignore` covers `.env`
- [x] `CLAUDE.md`, `README.md`, `docs/specs/config.md`
- [x] Install Docker on the VPS (000-D8)
- [x] Build the image from a clean checkout, and run it through Compose
- [x] Keep imported source docs out of the public repo and its history (000-D7)
