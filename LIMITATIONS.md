# Limitations

Everything here is a property of **Plane v1.3.1's public REST API**
(`/api/v1`, `X-API-Key` — the surface under `apps/api/plane/api/` in the
monorepo), verified against the version-matched source. The internal app API
(session auth, used by the web frontend) is richer; this CLI deliberately
never falls back to it.

## Missing from the public API

- **No workspace list/read endpoint.** Workspaces exist only as URL slugs;
  the CLI takes the slug from config. `plane workspace members` and
  `plane me` are the only workspace-level reads.
- **No server-side issue filtering.** The issue list endpoint accepts only
  `order_by`, `fields`, `expand`, and pagination params — no
  state/priority/label/assignee filters (the internal API has them).
  `plane issue list --priority/--state/--label/--assignee` therefore filter
  **client-side** after draining, reported in `meta.filtered`. On huge
  projects this fetches everything first.
- **No issue-type ("Epic") endpoints.** Work-item types are visible as
  `type_id` on issues but cannot be listed/managed. Pass `--data
  '{"type_id":"<uuid>"}'` if you know the UUID from elsewhere.
- **No sub-issue listing.** `parent` can be set on create/update, but there
  is no children query; the search/list endpoints don't expose it as a
  filter.
- **No intake, pages, worklogs, epics, initiatives, relations management
  beyond create/list** (relations create/list exists in the API but was out
  of the v1 CLI scope), no estimates CRUD in the CLI (endpoint exists),
  no member management in the CLI (project-member CRUD exists in the API;
  workspace members are read-only there too).
- **No upsert.** The API has PUT handlers in code but no PUT routes.
  Idempotent creation is only achievable via `external_id` +
  `external_source`: creates conflict with HTTP 409 and return the existing
  id in `error.details.id`.

## Server quirks the CLI absorbs or surfaces

- **Project names/identifiers reject most punctuation** (regex forbids
  `& + , : ; $ ^ } { * = ? @ # | ' < > . ( ) % ! -`). Hyphens in project
  names are rejected — hence smoke projects named `plane_cli_smoke_<ts>`.
- **Cycle create requires `project_id` in the body** even though it's in
  the URL; the CLI injects it automatically.
- **New projects may not have cycles/modules enabled** (instance-dependent);
  pass `--data '{"cycle_view":true,"module_view":true}'` on `project create`
  if you plan to use them.
- **State create returns HTTP 200** (not 201). Label/project/issue creates
  return 201. Cycle/module "add-issues" return 200 with the **full** set of
  link objects for the parent, not just the added ones.
- **Deletes are permission-gated**: issues (creator or project admin),
  cycles (owner or admin), modules (creator or admin), projects (project
  admin) — otherwise HTTP 403 (exit 3).
- **States**: the Triage state is hidden and cannot be created/updated;
  the default state and non-empty states cannot be deleted (HTTP 400).
- **Cycles**: dates are both-or-neither; completed cycles only accept
  `sort_order` updates and refuse new issues; only ended cycles can be
  archived (archiving a dateless draft cycle 500s server-side).
- **Modules**: only `completed`/`cancelled` modules can be archived.
  Duplicate module names fail with 400 (not 409 like states/labels).
- **Attachments**: server enforces a MIME allow-list and a size cap
  (`FILE_SIZE_LIMIT`, default 5 MB) — larger files are silently clamped in
  the presign policy, so storage rejects the actual upload. The
  attachment-list endpoint returns metadata only; download URLs come from
  the detail endpoint's 302 redirect (presigned, expires ~1h). Deletes are
  soft.
- **Rate limit**: 60 req/min per API key (300 for service tokens);
  `X-RateLimit-Remaining`/`X-RateLimit-Reset` headers are honored by the
  CLI's backoff.
- **`order_by` is ignored** by the links/comments list endpoints (always
  `-created_at`); it works on issues/projects/activities.
- **Search is a convenience endpoint**: returns at most `limit` (default
  10, CLI default 25) lightweight rows, not full issues, and matches only
  name substring / sequence number / project identifier.

## CLI-level notes

- `--cursor` resume granularity is per server page: resuming after a
  mid-page `--limit` cut re-reads that page (documented in
  `plane <resource> list --help` via `meta.pagination`).
- Bulk stdin bodies are sent verbatim — name→ID resolution applies to flag
  values only, except the `issue` key in `plane issue update -` lines.
- `attachment download` writes to a file (never stdout), keeping the
  stdout-is-JSON contract.
