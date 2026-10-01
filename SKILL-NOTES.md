# SKILL-NOTES — workflows an agent skill should document

Raw material for a `plane-cli` agent skill. Assumes `PLANE_BASE_URL`,
`PLANE_API_KEY` (or `PLANE_API_KEY_FILE`), `PLANE_WORKSPACE` are configured;
add `PLANE_PROJECT=<proj>` to drop the `-p` flags below. Every command
emits one JSON envelope on stdout; check `.ok` / exit code, parse with `jq`.

## 1. Sanity / auth check
```sh
plane me
```
Exit 0 with your user object, exit 3 on a bad key, exit 2 on missing config.

## 2. Find the right project
```sh
plane project list --fields id,name,identifier
plane project get MYPROJ
```

## 3. Create an issue (the everyday one)
```sh
plane issue create -p MYPROJ --name "Fix login redirect" \
  --priority high --state Todo --label bug \
  --description-html "<p>Steps: ...</p>"
```
State and label accept names; the created issue (with `id` and
`sequence_id`) is in `.data`.

## 4. Read an issue by its human identifier
```sh
plane issue get MYPROJ-42
plane issue get MYPROJ-42 --expand state,assignees,labels
```
No `--project` needed — the identifier carries it.

## 5. List and filter issues
```sh
plane issue list -p MYPROJ --limit 50 --order-by -created_at
plane issue list -p MYPROJ --state "In Progress" --priority high
```
Filters are client-side (public API limitation); `meta.filtered` shows
before/after counts. `--fields` only selects output columns: it never changes
which issues match a filter, so `--state Backlog --fields id,name` and
`--state Backlog` return the same issues. Resume capped lists with
`--cursor "$(jq -r .meta.pagination.next_cursor)"`.

## 6. Search across the workspace
```sh
plane issue search "login redirect" --workspace-wide --limit 25
```
Returns lightweight rows (`id`, `sequence_id`, `project__identifier`) —
follow up with `plane issue get`.

## 7. Move an issue through the workflow
```sh
plane issue update MYPROJ-42 --state "In Progress"
plane issue update MYPROJ-42 --state Done --priority none
```

## 8. Comment on and annotate an issue
```sh
plane comment add MYPROJ-42 --text "Deployed fix to staging"
plane link add MYPROJ-42 --url https://ci.example/run/123 --title "CI run"
```
`--text` is HTML-escaped and wrapped in `<p>`; use `--html` for markup.

## 9. Attach and retrieve files
```sh
plane attachment upload MYPROJ-42 --file ./crash.log --type text/plain
plane attachment list MYPROJ-42
plane attachment download MYPROJ-42 <asset-id> --output /tmp/crash.log
```
Upload is the full presigned flow (create → storage POST → confirm) in one
command. Mind the 5 MB default server cap and MIME allow-list.

## 10. Bulk import / bulk update (agents' bread and butter)
```sh
jq -c '.[]' backlog.json | plane issue create -p MYPROJ -
printf '%s\n' '{"issue":"MYPROJ-42","priority":"low"}' | plane issue update -
plane issue delete MYPROJ-51 MYPROJ-52 MYPROJ-53
```
Newline-delimited JSON on stdin; per-line outcomes in the envelope; any
failure → nonzero exit with `error.details.results`.

## 11. Idempotent creation from an external system
```sh
plane issue create -p MYPROJ --name "Alert 123" \
  --data '{"external_source":"alertmanager","external_id":"alert-123"}'
```
Re-running yields HTTP 409 / exit 5 with the existing issue's id in
`error.details.id` — check that instead of pre-querying. Look up later with
`plane issue get --external-source alertmanager --external-id alert-123`.

## 12. Sprint (cycle) management
```sh
plane cycle create -p MYPROJ --name "Sprint 14" --start-date 2026-08-01 --end-date 2026-08-14
plane cycle add-issues "Sprint 14" MYPROJ-42 MYPROJ-43 -p MYPROJ
plane cycle list-issues "Sprint 14" -p MYPROJ
plane cycle transfer-issues "Sprint 13" "Sprint 14" -p MYPROJ   # rollover (source must be ended)
```

## 13. Module (epic-ish grouping) management
```sh
plane module create -p MYPROJ --name "Auth overhaul" --status in-progress
plane module add-issues "Auth overhaul" MYPROJ-42 MYPROJ-44 -p MYPROJ
plane module update "Auth overhaul" -p MYPROJ --status completed
```

## 14. Project workflow setup (states & labels)
```sh
plane state create -p MYPROJ --name Review --color "#F59E0B" --group started
plane label create -p MYPROJ --name bug --color "#DC2626"
plane state list -p MYPROJ --fields id,name,group
```

## 15. Scripted health/report checks under systemd
```sh
plane project summary MYPROJ --count-fields issues,cycles
plane issue list -p MYPROJ --state Todo --fields id,name | jq '.data | length'
```
Non-interactive guarantees: exit 2 fast on missing config, 7 on rate-limit
exhaustion; stderr is journald-safe, stdout is exactly one JSON document.

## Skill-authoring hints
- Teach the exit-code table (0/2/3/4/5/6/7) and `.error.code` values
  (`usage`, `config`, `auth`, `not_found`, `validation`, `conflict`,
  `ambiguous`, `rate_limited`, `server`, `network`).
- Teach `meta.resolved` (what names became which UUIDs) and
  `error.details.candidates` for disambiguation loops.
- Prefer UUIDs (from earlier responses) over names in loops — resolution
  costs an extra list request against a 60 req/min budget.
- `--data` is the escape hatch for any serializer field the flags don't
  cover; flags win over `--data` on conflict.
- Project names reject most punctuation (incl. `-`); identifiers are ≤12
  chars, auto-uppercased.
