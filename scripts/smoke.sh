#!/usr/bin/env bash
# Live smoke test for plane-cli against a real Plane instance.
#
# Requires: PLANE_BASE_URL, PLANE_API_KEY (or PLANE_API_KEY_FILE),
# PLANE_WORKSPACE in the environment, jq on PATH, and ./plane built
# (run `go build -o plane .` first or pass PLANE_BIN).
#
# Everything happens inside a scratch project named plane-cli-smoke-<ts>,
# which is deleted at the end (also on failure). No other project is touched.
set -u

PLANE=${PLANE_BIN:-./plane}
TS=$(date +%s)
# Plane v1.3.1 forbids most punctuation (incl. "-") in project names, so the
# scratch project uses underscores.
SCRATCH="plane_cli_smoke_$TS"
IDENT="SMK$(date +%H%M%S)"
PROJECT_ID=""
FAILED=0
STEP=""

log() { printf '%s\n' "$*" >&2; }

# run <step-name> <expected-exit> <args...>  — runs plane, checks exit code,
# stores stdout in $OUT.
run() {
  STEP=$1; local want=$2; shift 2
  OUT=$("$PLANE" "$@" 2>/tmp/smoke-stderr.$$)
  local got=$?
  if [ "$got" -ne "$want" ]; then
    log "FAIL [$STEP] exit=$got want=$want"
    log "  args: $*"
    log "  stdout: $OUT"
    log "  stderr: $(tail -3 /tmp/smoke-stderr.$$ 2>/dev/null)"
    FAILED=1
    return 1
  fi
  if ! jq -e . >/dev/null 2>&1 <<<"$OUT"; then
    log "FAIL [$STEP] stdout is not JSON: $OUT"
    FAILED=1
    return 1
  fi
  log "ok   [$STEP]"
  return 0
}

# jqo <filter> — extract from last $OUT
jqo() { jq -r "$1" <<<"$OUT"; }

cleanup() {
  if [ -n "$PROJECT_ID" ]; then
    log "cleanup: deleting scratch project $PROJECT_ID"
    "$PLANE" project delete "$PROJECT_ID" >/dev/null 2>&1 || log "cleanup: project delete failed (manual cleanup may be needed: $SCRATCH)"
  fi
  rm -f /tmp/smoke-stderr.$$ /tmp/plane-smoke-note.$$.txt /tmp/plane-smoke-dl.$$.txt
}
trap cleanup EXIT

log "=== plane-cli live smoke: scratch project $SCRATCH ($IDENT) ==="

### auth / read-only sanity
run "me" 0 me || exit 1
run "workspace members" 0 workspace members || exit 1

### project lifecycle
# cycle_view/module_view are not guaranteed on by default; enable explicitly.
run "project create" 0 project create --name "$SCRATCH" --identifier "$IDENT" \
  --data '{"cycle_view":true,"module_view":true}' || exit 1
PROJECT_ID=$(jqo '.data.id')
[ -n "$PROJECT_ID" ] && [ "$PROJECT_ID" != "null" ] || { log "FAIL no project id"; exit 1; }
log "     scratch project id: $PROJECT_ID"

run "project get by name (resolution)" 0 project get "$SCRATCH"
run "project update" 0 project update "$PROJECT_ID" --description "smoke scratch"
run "project summary" 0 project summary "$PROJECT_ID"
run "project list --limit 2" 0 project list --limit 2

### states
run "state list (defaults)" 0 state list -p "$PROJECT_ID"
run "state create Review" 0 state create -p "$PROJECT_ID" --name "Review" --color "#F59E0B" --group started
STATE_REVIEW=$(jqo '.data.id')
run "state get by name" 0 state get review -p "$PROJECT_ID"
run "state update color" 0 state update "$STATE_REVIEW" -p "$PROJECT_ID" --color "#00AA00"
run "state create duplicate -> 409/exit5" 5 state create -p "$PROJECT_ID" --name "Review" --color "#F59E0B"

### labels
run "label create bug" 0 label create -p "$PROJECT_ID" --name bug --color "#DC2626"
LABEL_BUG=$(jqo '.data.id')
run "label create backend" 0 label create -p "$PROJECT_ID" --name backend
LABEL_BACKEND=$(jqo '.data.id')
run "label update" 0 label update "$LABEL_BUG" -p "$PROJECT_ID" --description "defects"
run "label list" 0 label list -p "$PROJECT_ID"

### issues
run "issue create #1 (state+label by name)" 0 issue create -p "$PROJECT_ID" \
  --name "smoke issue one" --priority high --state todo --label bug \
  --description-html "<p>first smoke issue</p>"
ISSUE1=$(jqo '.data.id')
ISSUE1_SEQ=$(jqo '.data.sequence_id')
run "issue create #2" 0 issue create -p "$PROJECT_ID" --name "smoke issue two"
ISSUE2=$(jqo '.data.id')

run "issue list" 0 issue list -p "$PROJECT_ID"
run "issue list client filter --priority high" 0 issue list -p "$PROJECT_ID" --priority high
[ "$(jqo '.data | length')" = "1" ] || { log "FAIL filter expected 1 result, got $(jqo '.data | length')"; FAILED=1; }

run "issue get by identifier" 0 issue get "$IDENT-$ISSUE1_SEQ"
run "issue get by uuid" 0 issue get "$ISSUE1" -p "$PROJECT_ID"
run "issue update" 0 issue update "$ISSUE1" -p "$PROJECT_ID" --priority medium --target-date 2026-12-31
run "issue search" 0 issue search "smoke issue" -p "$PROJECT_ID"

# bulk stdin create + update + multi-delete
run "issue bulk create (stdin)" 0 issue create -p "$PROJECT_ID" - <<'EOF'
{"name":"bulk a","priority":"low"}
{"name":"bulk b"}
EOF
BULK_A=$(jqo '.data.results[0].data.id')
BULK_B=$(jqo '.data.results[1].data.id')
run "issue bulk update (stdin)" 0 issue update -p "$PROJECT_ID" - <<EOF
{"issue":"$BULK_A","priority":"none"}
EOF
run "issue multi-delete" 0 issue delete "$BULK_A" "$BULK_B" -p "$PROJECT_ID"

### comments
run "comment add" 0 comment add "$ISSUE1" -p "$PROJECT_ID" --text "deployed & <verified>"
COMMENT1=$(jqo '.data.id')
run "comment list" 0 comment list "$ISSUE1" -p "$PROJECT_ID"
run "comment get" 0 comment get "$ISSUE1" "$COMMENT1" -p "$PROJECT_ID"
run "comment update" 0 comment update "$ISSUE1" "$COMMENT1" -p "$PROJECT_ID" --html "<p>edited</p>"
run "comment delete" 0 comment delete "$ISSUE1" "$COMMENT1" -p "$PROJECT_ID"

### links
run "link add" 0 link add "$ISSUE1" -p "$PROJECT_ID" --url "https://example.com/run/$TS" --title "CI run"
LINK1=$(jqo '.data.id')
run "link list" 0 link list "$ISSUE1" -p "$PROJECT_ID"
run "link get" 0 link get "$ISSUE1" "$LINK1" -p "$PROJECT_ID"
run "link update" 0 link update "$ISSUE1" "$LINK1" -p "$PROJECT_ID" --title "CI run (final)"
run "link duplicate url -> exit5" 5 link add "$ISSUE1" -p "$PROJECT_ID" --url "https://example.com/run/$TS"
run "link delete" 0 link delete "$ISSUE1" "$LINK1" -p "$PROJECT_ID"

### attachments (full presigned flow)
NOTE=/tmp/plane-smoke-note.$$.txt
printf 'smoke attachment payload %s\n' "$TS" > "$NOTE"
run "attachment upload" 0 attachment upload "$ISSUE1" -p "$PROJECT_ID" --file "$NOTE" --type text/plain
ASSET1=$(jqo '.data.asset_id')
run "attachment list" 0 attachment list "$ISSUE1" -p "$PROJECT_ID"
run "attachment get url" 0 attachment get "$ISSUE1" "$ASSET1" -p "$PROJECT_ID"
DL=/tmp/plane-smoke-dl.$$.txt
run "attachment download" 0 attachment download "$ISSUE1" "$ASSET1" -p "$PROJECT_ID" --output "$DL"
if ! cmp -s "$NOTE" "$DL"; then
  log "FAIL attachment roundtrip: downloaded bytes differ"
  FAILED=1
fi
run "attachment delete" 0 attachment delete "$ISSUE1" "$ASSET1" -p "$PROJECT_ID"

### cycles
TODAY=$(date +%F)
NEXTWEEK=$(date -d "+7 days" +%F)
LASTMONTH_START=$(date -d "-21 days" +%F)
LASTMONTH_END=$(date -d "-14 days" +%F)

run "cycle create current" 0 cycle create -p "$PROJECT_ID" --name "smoke sprint" \
  --start-date "$TODAY" --end-date "$NEXTWEEK"
CYCLE1=$(jqo '.data.id')
run "cycle create past" 0 cycle create -p "$PROJECT_ID" --name "smoke past sprint" \
  --start-date "$LASTMONTH_START" --end-date "$LASTMONTH_END"
CYCLE_PAST=$(jqo '.data.id')

run "cycle get by name" 0 cycle get "smoke sprint" -p "$PROJECT_ID"
run "cycle update" 0 cycle update "$CYCLE1" -p "$PROJECT_ID" --description "smoke cycle"
run "cycle add-issues" 0 cycle add-issues "$CYCLE1" "$ISSUE1" "$ISSUE2" -p "$PROJECT_ID"
run "cycle list-issues" 0 cycle list-issues "$CYCLE1" -p "$PROJECT_ID"
[ "$(jqo '.data | length')" = "2" ] || { log "FAIL cycle should contain 2 issues"; FAILED=1; }
run "cycle remove-issue" 0 cycle remove-issue "$CYCLE1" "$ISSUE2" -p "$PROJECT_ID"
run "cycle transfer-issues (past -> current)" 0 cycle transfer-issues "$CYCLE_PAST" "$CYCLE1" -p "$PROJECT_ID"
run "cycle list" 0 cycle list -p "$PROJECT_ID"
run "cycle list --view current" 0 cycle list -p "$PROJECT_ID" --view current
run "cycle archive past" 0 cycle archive "$CYCLE_PAST" -p "$PROJECT_ID"
run "cycle list-archived" 0 cycle list-archived -p "$PROJECT_ID"
run "cycle unarchive" 0 cycle unarchive "$CYCLE_PAST" -p "$PROJECT_ID"
run "cycle delete past" 0 cycle delete "$CYCLE_PAST" -p "$PROJECT_ID"
run "cycle delete current" 0 cycle delete "$CYCLE1" -p "$PROJECT_ID"

### modules
run "module create" 0 module create -p "$PROJECT_ID" --name "smoke module" --status in-progress
MODULE1=$(jqo '.data.id')
run "module get by name" 0 module get "smoke module" -p "$PROJECT_ID"
run "module add-issues" 0 module add-issues "$MODULE1" "$ISSUE1" "$ISSUE2" -p "$PROJECT_ID"
run "module list-issues" 0 module list-issues "$MODULE1" -p "$PROJECT_ID"
[ "$(jqo '.data | length')" = "2" ] || { log "FAIL module should contain 2 issues"; FAILED=1; }
run "module remove-issue" 0 module remove-issue "$MODULE1" "$ISSUE2" -p "$PROJECT_ID"
run "module update -> completed" 0 module update "$MODULE1" -p "$PROJECT_ID" --status completed
run "module archive" 0 module archive "$MODULE1" -p "$PROJECT_ID"
run "module list-archived" 0 module list-archived -p "$PROJECT_ID"
run "module unarchive" 0 module unarchive "$MODULE1" -p "$PROJECT_ID"
run "module delete" 0 module delete "$MODULE1" -p "$PROJECT_ID"

### deletions / protections
run "issue delete #1" 0 issue delete "$ISSUE1" -p "$PROJECT_ID"
run "issue delete #2" 0 issue delete "$ISSUE2" -p "$PROJECT_ID"
run "state delete Review (empty)" 0 state delete "$STATE_REVIEW" -p "$PROJECT_ID"
run "label delete backend" 0 label delete "$LABEL_BACKEND" -p "$PROJECT_ID"
run "issue get deleted -> exit4" 4 issue get "$ISSUE1" -p "$PROJECT_ID"

### project archive/unarchive, then final delete (also exercised by cleanup)
run "project archive" 0 project archive "$PROJECT_ID"
run "project unarchive" 0 project unarchive "$PROJECT_ID"
run "project delete" 0 project delete "$PROJECT_ID"
PROJECT_ID=""

if [ "$FAILED" -ne 0 ]; then
  log "=== SMOKE FAILED ==="
  exit 1
fi
log "=== SMOKE PASSED ==="
