# Authentication

Interviewers sign in with a 6-digit code emailed to an allowed address (`internal/auth`, `internal/server`, `internal/mail`). The request endpoint never reveals whether an address is allowed: status, body, and response time are the same either way (001-D11).

## Flow

1. `GET /login` serves the login page. If the visitor already has a live session, it redirects to `/`.
2. `POST /api/auth/request` `{"email": "..."}`
   - Checks the per-IP limit (`CODE_REQUESTS_PER_IP_HOUR`). This counts every request, including ones for disallowed or malformed emails.
   - Normalises the email (trimmed, lower-case, bare address only).
   - Checks the per-email limit (`CODE_REQUESTS_PER_EMAIL_HOUR`). This applies to every address, allowed or not.
   - Checks the email against `ALLOWED_EMAILS` and `ALLOWED_EMAIL_DOMAINS` (exact domain match). A disallowed email gets the same `200` response as an allowed one, and nothing is sent (001-D11).
   - Generates a code with `crypto/rand` and stores an HMAC-SHA256 of it under a random per-process key, valid for `CODE_TTL`. A new code replaces any earlier one for that email.
   - Responds `200 {"ok": true, "expires_in_seconds": N}`, then sends a plain-text email in the background (subject "Your sign-in code", the code, the expiry, no links). If sending fails, the code is discarded and the failure is logged. Shutdown waits for in-flight sends.
3. `POST /api/auth/verify` `{"email": "...", "code": "..."}`
   - Checks the per-IP limit (`VERIFY_PER_IP_HOUR`).
   - Compares the MAC in constant time. A wrong guess counts against the code; after `VERIFY_ATTEMPTS_PER_CODE` wrong guesses the code is discarded. An expired code is discarded.
   - On success the code is deleted (single use). A 256-bit random session token is issued, stored server-side as SHA-256, and set as cookie `__Host-session`: `HttpOnly`, `Secure`, `SameSite=Strict`, `Path=/`, `Max-Age` = `SESSION_TTL`.
4. `POST /api/auth/logout` deletes the session and clears the cookie.

Codes, sessions, and limits live in memory and are pruned every minute. A restart signs everyone out.

## Route protection

Routing is default-deny. Public routes: `/login`, `/healthz`, `/robots.txt`, `/static/*`, `/api/auth/request`, `/api/auth/verify`, `/api/auth/logout`. Every other route is registered on `Server.Protected` behind `requireAuth`:

- API paths (`/api/...`) and non-GET requests without a live session get `401 {"error":"unauthenticated"}`.
- Page GETs without a live session redirect (`303`) to `/login`.

`GET /api/me` returns the signed-in email and the remaining chat turns (`SESSION_TURN_CAP`).

## Errors

JSON error bodies are `{"error": code, "message": text}`.

| Status | `error` | When |
| --- | --- | --- |
| 400 | `invalid_email` | Malformed email |
| 400 | `bad_request` | Malformed JSON or a body over 4 KB |
| 401 | `invalid_code` | Wrong, expired, spent, or unknown code |
| 401 | `unauthenticated` | Protected API route without a session |
| 415 | `unsupported_media_type` | POST body isn't `application/json` (CSRF defence) |
| 429 | `rate_limited` | Any limit hit; includes a `Retry-After` header and `retry_after_seconds` |

## Email transport

`internal/mail` connects to `SMTP_HOST:SMTP_PORT`, requires STARTTLS (TLS 1.2 or later, certificate verified against the host name), then authenticates with AUTH PLAIN as `SMTP_USER`. It refuses to continue if the server doesn't offer STARTTLS. Errors never include the password or the message body. The whole exchange times out after 30 seconds.

## Headers

Every response carries `X-Robots-Tag: noindex, nofollow`, `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and `Cache-Control: no-store`.

## Logging

Logged: `code_sent` and `signed_in` (with the email), `code_rejected_not_allowed`, `code_rate_limited`, and `verify_rate_limited` (with the IP), and `code_send_failed` (with the error). Logs are server-side only, so they may record what the response hides. Never logged: codes, session tokens, the SMTP password, or API keys.

## Client IP

By default the client IP is the TCP peer address. With `TRUST_FORWARDED_FOR=true`, it is the last entry of the last `X-Forwarded-For` header. Enable that only behind a proxy that sets the header.
