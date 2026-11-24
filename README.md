# interview-project-agent

A private, read-only web chat for interviewers. They sign in with an emailed one-time code and ask about a project's code and the approach taken. Answers come from a read-only snapshot of the code and its paper trail. Each milestone's design and decisions are in [docs/changes](docs/changes), and built behaviour is in [docs/specs](docs/specs).

> Status: M0 (scaffold). The server loads and validates its configuration and serves `/healthz`. Login, chat, and deployment arrive in M1 to M4.

## Configuration

All settings come from environment variables; see [.env.example](.env.example) and [docs/specs/config.md](docs/specs/config.md). The server refuses to start (exit code 2) if a required value is missing or invalid, and lists every problem at once. Secret values are never printed.

## Run locally

```sh
go test ./...
CGO_ENABLED=0 go build -o bin/server ./cmd/server
set -a; . ./.env; set +a; SNAPSHOT_DIR=/path/to/snapshot ./bin/server
curl localhost:8080/healthz
```

## Container

```sh
cp .env.example .env   # then fill it in
docker compose build
docker compose up -d
```

The container runs as a non-root user (uid 65532) on a read-only root filesystem, with all capabilities dropped and a 256 MB memory cap. The snapshot directory (`SNAPSHOT_HOST_DIR`) is mounted read-only at `/snapshot`, and `DATA_HOST_DIR` is mounted writable at `/data`.
