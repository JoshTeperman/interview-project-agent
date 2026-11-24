# interview-project-agent

A private, read-only web chat for interviewers. They sign in with an emailed one-time code and ask about a project's code and the approach taken. Answers come from a read-only snapshot of the code and its paper trail. Each milestone's design and decisions are in [docs/changes](docs/changes), and built behaviour is in [docs/specs](docs/specs).

> Status: M2 (snapshot and agent). Interviewers sign in with an emailed code and can ask questions through a JSON API (`POST /api/chat`); the chat UI arrives in M3 and deployment in M4. See [docs/specs/auth.md](docs/specs/auth.md) and [docs/specs/agent.md](docs/specs/agent.md).

## Configuration

All settings come from environment variables; see [.env.example](.env.example) and [docs/specs/config.md](docs/specs/config.md). The server refuses to start (exit code 2) if a required value is missing or invalid, and lists every problem at once. Secret values are never printed.

## Run locally

```sh
go test ./...
CGO_ENABLED=0 go build -o bin/server ./cmd/server
set -a; . ./.env; set +a; SNAPSHOT_DIR=/path/to/snapshot ./bin/server
curl localhost:8080/healthz
```

## Compare providers

`cmd/compare` asks the same questions of OpenAI and Anthropic (whichever have API keys set) and writes a Markdown report. It calls the real APIs, so each run is billed.

```sh
go run ./cmd/compare -snapshot evaluation/fixtures/tiny-kv -questions evaluation/questions.txt -out compare.md
```

## Container

```sh
cp .env.example .env   # then fill it in
docker compose build
docker compose up -d
```

The container runs as a non-root user (uid 65532) on a read-only root filesystem, with all capabilities dropped and a 256 MB memory cap. The snapshot directory (`SNAPSHOT_HOST_DIR`) is mounted read-only at `/snapshot`, and `DATA_HOST_DIR` is mounted writable at `/data`.
