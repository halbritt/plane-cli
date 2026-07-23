# plane-cli

A JSON-first Go CLI for the **public REST API** (`/api/v1`, `X-API-Key`) of a
self-hosted [Plane](https://github.com/makeplane/plane) instance. Built for
**non-interactive** consumers: coding agents driving Plane through bash, and
headless `systemd` units/timers.

Built against the API surface of **Plane v1.3.1**, derived from the
version-matched apiserver source (`apps/api/plane/api/`), not from docs.
See `LIMITATIONS.md` for what the public API cannot do.

## Install

```sh
cd ~/git/plane-cli
go build -o plane .
install -m 0755 plane ~/.local/bin/plane   # or anywhere on PATH
```

## Output contract

Exactly one JSON envelope on stdout per invocation — never anything else.
Human diagnostics and `--debug` tracing go to stderr (journald-friendly).
The binary never prompts and never waits on a TTY.

```json
{"ok": true,  "data": ..., "meta": {...}}
{"ok": false, "error": {"code": "...", "message": "...", "http_status": 404, "details": ...}}
```

- `data` is the server's payload **verbatim** (no re-shaping, no dropped
  fields), except list commands, where `data` is the drained results array
  and `meta.pagination` carries `{total_count, fetched, pages, drained,
  next_cursor?}`.
- `meta.resolved` records any name→ID resolutions performed.
- `error.details` carries the raw server error body (or, for ambiguous
  names, a `candidates` list).

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | success |
| 2 | usage or configuration error (bad flags, missing key/base URL) |
| 3 | authentication/authorization failed (HTTP 401/403) |
| 4 | not found (HTTP 404, incl. unresolvable names) |
| 5 | validation (other HTTP 4xx incl. 409 conflict; ambiguous names) |
| 6 | server or network error (HTTP 5xx, timeouts, connection failures) |
| 7 | rate limited and retries exhausted (HTTP 429) |

## Configuration

Precedence: **flags > environment > config file**.

| Env var | Meaning |
|---------|---------|
| `PLANE_BASE_URL` | e.g. `https://plane.example.com` |
| `PLANE_API_KEY` | API key (create in Plane: Profile → Personal Access Tokens) |
| `PLANE_API_KEY_FILE` | path to a file containing the key (trailing whitespace stripped); wins over `PLANE_API_KEY` |
| `PLANE_WORKSPACE` | workspace slug |
| `PLANE_PROJECT` | default project (name, identifier, or UUID) |
| `PLANE_CONFIG` | config file path override |

The API key is **never** accepted as a command-line argument and never
appears in logs or `--debug` output.

Config file (`~/.config/plane-cli/config.toml`):

```toml
base_url  = "https://plane.example.com"
workspace = "myworkspace"
project   = "MYPROJ"          # optional default project
api_key_file = "/etc/plane-cli/key"   # prefer over inline api_key

# Optional per-project overrides, applied when the selected project matches
# (case-insensitive):
[project_overrides."Side Project"]
workspace = "other-workspace"
```

## systemd usage

`Type=oneshot` + `LoadCredential=` keeps the key out of the environment and
argv; missing config fails fast with exit 2 instead of hanging.

```ini
# /etc/systemd/system/plane-report.service
[Unit]
Description=File a Plane issue from a nightly check

[Service]
Type=oneshot
User=svc-plane
LoadCredential=plane-api-key:/etc/credstore/plane-api-key
Environment=PLANE_BASE_URL=https://plane.example.com
Environment=PLANE_WORKSPACE=myworkspace
Environment=PLANE_API_KEY_FILE=%d/plane-api-key
ExecStart=/usr/local/bin/plane issue create -p OPS --name "Nightly check failed"
```

```ini
# /etc/systemd/system/plane-report.timer
[Timer]
OnCalendar=*-*-* 06:00:00
Persistent=true

[Install]
WantedBy=timers.target
```

## Agent usage notes

- Every subcommand's `--help` includes worked examples; the tool is
  operable from help output alone (`plane --help`, `plane issue --help`,
  `plane issue create --help`, …).
- Parse only stdout; treat stderr as free-form diagnostics.
- Check `.ok` and the exit code; on failure read `.error.code` /
  `.error.details`.
- Names are accepted wherever the API wants UUIDs: project name or
  identifier, state/label/cycle/module names (case-insensitive), and
  `PROJ-123` work-item identifiers. Ambiguity fails (exit 5) with
  `error.details.candidates`. `--no-resolve` disables this and requires
  UUIDs (fewer lookup requests).
- `PROJ-123` references carry their own project; `--project` is only needed
  with bare UUIDs or project-scoped lists.
- Lists auto-drain all pages (server pages are ≤1000 items). `--limit N`
  caps output; resume with `--cursor <meta.pagination.next_cursor>`.
- The instance rate limit is 60 requests/min per key; the CLI backs off on
  429 automatically (bounded by `--max-retries`). Prefer UUIDs over names
  in tight loops to avoid resolution lookups.
- Bulk input: `plane issue create -` / `plane issue update -` read
  newline-delimited JSON from stdin; `plane issue delete` accepts many refs.
- Server-side write bodies can always be extended with `--data '<json>'`
  (explicit flags win on conflict).

## Retry semantics

- HTTP 429: retried for **all** methods (the request was never processed),
  honoring `Retry-After`/`X-RateLimit-Reset`, with exponential backoff and
  jitter, up to `--max-retries` (default 4). Exhaustion → exit 7.
- HTTP 5xx and network errors: retried for idempotent methods (GET/DELETE)
  only, unless `--retry-unsafe` is passed.

## Development

```sh
go test ./...        # contract tests (httptest fixtures from v1.3.1 serializers)
go vet ./... && gofmt -l .
scripts/smoke.sh     # live happy-path run in a scratch project (needs env)
```
