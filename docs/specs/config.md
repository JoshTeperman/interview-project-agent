# Configuration

The server reads all settings from environment variables at startup (`internal/config`). Loading fails closed: any missing or invalid required value stops the process with exit code 2, and the log lists every problem. Secret values (API keys, SMTP password) never appear in errors or logs; the startup log shows them only as `<set>` or `<unset>`.

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `ALLOWED_EMAIL_DOMAINS` | one of these two | | Comma-separated, case-insensitive, leading `@` optional. Exact domain match; subdomains are not included. |
| `ALLOWED_EMAILS` | one of these two | | Comma-separated explicit allowlist, case-insensitive. |
| `LLM_PROVIDER` | no | `openai` | `openai` or `anthropic`. |
| `LLM_MODEL` | no | `gpt-6-luna` / `claude-haiku-5-5` | The default depends on the provider. |
| `OPENAI_API_KEY` | if provider is `openai` | | Dedicated key with a hard spend cap. |
| `ANTHROPIC_API_KEY` | if provider is `anthropic` | | Dedicated key with a hard spend cap. |
| `SMTP_HOST` | no | `smtp.gmail.com` | |
| `SMTP_PORT` | no | `587` | STARTTLS. |
| `SMTP_USER` | yes | | |
| `SMTP_PASS` | yes | | Google App Password. |
| `SMTP_FROM_NAME` | no | `Josh Projects Guide` | |
| `SNAPSHOT_DIR` | yes | | Must be an existing directory. Compose sets it to `/snapshot`. |
| `DATA_DIR` | no | empty | Writable directory for the question log. Compose sets it to `/data`. |
| `LISTEN_ADDR` | no | `:8080` | |
| `CODE_TTL` | no | `10m` | Go duration. |
| `SESSION_TTL` | no | `12h` | Go duration. |
| `CODE_REQUESTS_PER_IP_HOUR` | no | `10` | |
| `CODE_REQUESTS_PER_EMAIL_HOUR` | no | `3` | |
| `VERIFY_ATTEMPTS_PER_CODE` | no | `5` | Wrong guesses before a code is discarded. |
| `VERIFY_PER_IP_HOUR` | no | `30` | Verification attempts per IP. |
| `CHAT_PER_SESSION_MINUTE` | no | `6` | |
| `CHAT_PER_IP_MINUTE` | no | `12` | |
| `MAX_MESSAGE_CHARS` | no | `2000` | |
| `SESSION_TURN_CAP` | no | `40` | Chat turns per session. |
| `TOOL_STEP_CAP` | no | `6` | Tool calls per turn (S-D2). |
| `TRUST_FORWARDED_FOR` | no | `false` | Take the client IP from the last `X-Forwarded-For` entry. Only behind a trusted proxy. |

All integer limits must be positive.

Compose also reads `SNAPSHOT_HOST_DIR` (required) and `DATA_HOST_DIR` (default `./data`) to choose the host directories it mounts.
