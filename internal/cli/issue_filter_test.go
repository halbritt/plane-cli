package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// PLANECLI-3: `issue list --state ... --fields ...` returned success with zero
// rows because --fields is a server-side sparse fieldset, so the records the
// client filtered had no state to match. These tests run a fake Plane that
// honors `fields` and `expand` the way the real API does.

const (
	filterProject = "11111111-1111-4111-8111-111111111111"
	stateBacklog  = "aaaaaaaa-0000-4000-8000-000000000001"
	stateTodo     = "aaaaaaaa-0000-4000-8000-000000000002"
	stateDone     = "aaaaaaaa-0000-4000-8000-000000000003"
	labelBug      = "bbbbbbbb-0000-4000-8000-000000000001"
	labelUI       = "bbbbbbbb-0000-4000-8000-000000000002"
	userOne       = "cccccccc-0000-4000-8000-000000000001"
	userTwo       = "cccccccc-0000-4000-8000-000000000002"
)

type fakeIssue struct {
	name     string
	state    string
	priority string
	labels   []string
	assignee []string
}

// The corpus: every filter has both matches and non-matches.
var corpus = []fakeIssue{
	{"issue-1", stateBacklog, "high", []string{labelBug}, []string{userOne}},
	{"issue-2", stateBacklog, "low", []string{labelUI}, nil},
	{"issue-3", stateTodo, "high", []string{labelBug, labelUI}, []string{userOne, userTwo}},
	{"issue-4", stateDone, "none", nil, []string{userTwo}},
	{"issue-5", stateBacklog, "high", nil, []string{userOne}},
	{"issue-6", stateBacklog, "medium", []string{labelBug}, nil},
}

type fakePlane struct {
	t        *testing.T
	omit     map[string]bool // fields the server drops even when asked for them
	pageSize int             // 0: one page
	requests []url.Values    // work-item list queries, in order
}

func (f *fakePlane) record(i int, issue fakeIssue, query url.Values) []byte {
	expand := strings.Split(query.Get("expand"), ",")
	relation := func(field, id, name string) any {
		if slices.Contains(expand, field) {
			return map[string]any{"id": id, "name": name}
		}
		return id
	}
	relations := func(field string, values []string, names map[string]string) any {
		out := []any{}
		for _, v := range values {
			out = append(out, relation(field, v, names[v]))
		}
		return out
	}
	full := map[string]any{
		"id": "id-" + strconv.Itoa(i), "name": issue.name, "sequence_id": i + 1, "priority": issue.priority,
		"state":     relation("state", issue.state, map[string]string{stateBacklog: "Backlog", stateTodo: "Todo", stateDone: "Done"}[issue.state]),
		"labels":    relations("labels", issue.labels, map[string]string{labelBug: "bug", labelUI: "ui"}),
		"assignees": relations("assignees", issue.assignee, map[string]string{userOne: "one", userTwo: "two"}),
		"point":     json.RawMessage("1.50"), "description_html": "<p>x</p>",
	}
	keys := []string{"id", "name", "sequence_id", "priority", "state", "labels", "assignees", "point", "description_html"}
	if requested := query.Get("fields"); requested != "" {
		keys = nil
		for _, name := range strings.Split(requested, ",") {
			if name != "" && !f.omit[name] {
				keys = append(keys, name)
			}
		}
	}
	// Emit in the requested order, as one JSON object, so key order is the server's.
	var out strings.Builder
	out.WriteByte('{')
	for n, key := range keys {
		value, ok := full[key]
		if !ok {
			continue
		}
		encoded, _ := json.Marshal(value)
		if n > 0 && out.Len() > 1 {
			out.WriteByte(',')
		}
		name, _ := json.Marshal(key)
		out.Write(name)
		out.WriteByte(':')
		out.Write(encoded)
	}
	out.WriteByte('}')
	return []byte(out.String())
}

func (f *fakePlane) server() *httptest.Server {
	projects := []map[string]any{{"id": filterProject, "name": "My Project", "identifier": "MYPROJ"}}
	base := "/api/v1/workspaces/ws/projects/" + filterProject + "/"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if projectListHandler(f.t, projects)(w, r) {
			return
		}
		switch {
		case r.URL.Path == base+"states/":
			writeJSON(w, 200, page([]any{
				map[string]any{"id": stateBacklog, "name": "Backlog", "group": "backlog"},
				map[string]any{"id": stateTodo, "name": "Todo", "group": "unstarted"},
				map[string]any{"id": stateDone, "name": "Done", "group": "completed"},
			}, "1000:1:0", false, 3))
		case r.URL.Path == base+"labels/":
			writeJSON(w, 200, page([]any{
				map[string]any{"id": labelBug, "name": "bug"}, map[string]any{"id": labelUI, "name": "ui"},
			}, "1000:1:0", false, 2))
		case r.URL.Path == base+"work-items/" && r.Method == "GET":
			query := r.URL.Query()
			f.requests = append(f.requests, query)
			start, end, next, more := 0, len(corpus), "1000:1:0", false
			if f.pageSize > 0 {
				pageNumber := 0
				if cursor := query.Get("cursor"); strings.HasPrefix(cursor, "p:") {
					pageNumber, _ = strconv.Atoi(strings.TrimPrefix(cursor, "p:"))
				}
				start = pageNumber * f.pageSize
				end = min(start+f.pageSize, len(corpus))
				more = end < len(corpus)
				next = "p:" + strconv.Itoa(pageNumber+1)
			}
			var raw []json.RawMessage
			for i := start; i < end; i++ {
				raw = append(raw, f.record(i, corpus[i], query))
			}
			w.Header().Set("Content-Type", "application/json")
			body, _ := json.Marshal(map[string]any{
				"next_cursor": next, "next_page_results": more, "count": len(raw),
				"total_pages": 1, "total_count": len(corpus), "total_results": len(corpus), "results": raw,
			})
			w.Write(body)
		default:
			f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			writeJSON(w, 404, map[string]any{"error": "The requested resource does not exist."})
		}
	}))
}

func listIssues(t *testing.T, f *fakePlane, args ...string) (runResult, map[string]any) {
	t.Helper()
	srv := f.server()
	defer srv.Close()
	res := run(t, srv.URL, nil, "", append([]string{"issue", "list", "-p", "myproj"}, args...)...)
	if res.stdout == "" {
		t.Fatalf("no output (exit %d, stderr %s)", res.code, res.stderr)
	}
	return res, decodeEnvelope(t, res.stdout)
}

func names(t *testing.T, env map[string]any, key string) []string {
	t.Helper()
	data, _ := env["data"].([]any)
	var found []string
	for _, row := range data {
		switch v := row.(map[string]any)[key].(type) {
		case string:
			found = append(found, v)
		case float64:
			found = append(found, "issue-"+strconv.Itoa(int(v)))
		default:
			t.Fatalf("record has no %q: %v", key, row)
		}
	}
	return found
}

func keysOf(row any) []string {
	var found []string
	for k := range row.(map[string]any) {
		found = append(found, k)
	}
	slices.Sort(found)
	return found
}

type filterCase struct {
	name  string
	flags []string
	want  []string // issue names
}

var filterCases = []filterCase{
	{"state", []string{"--state", "Backlog"}, []string{"issue-1", "issue-2", "issue-5", "issue-6"}},
	{"state by uuid", []string{"--state", stateBacklog}, []string{"issue-1", "issue-2", "issue-5", "issue-6"}},
	{"priority", []string{"--priority", "high"}, []string{"issue-1", "issue-3", "issue-5"}},
	{"label", []string{"--label", "bug"}, []string{"issue-1", "issue-3", "issue-6"}},
	{"assignee", []string{"--assignee", userOne}, []string{"issue-1", "issue-3", "issue-5"}},
	{"label in second position", []string{"--label", "ui"}, []string{"issue-2", "issue-3"}},
	{"assignee in second position", []string{"--assignee", userTwo}, []string{"issue-3", "issue-4"}},
	{"state and priority", []string{"--state", "Backlog", "--priority", "high"}, []string{"issue-1", "issue-5"}},
	{"all four", []string{"--state", "Backlog", "--priority", "high", "--label", "bug", "--assignee", userOne}, []string{"issue-1"}},
	{"none match", []string{"--priority", "urgent"}, nil},
}

// The reported failure: output field selection must never change membership.
func TestIssueFilterMembershipDoesNotDependOnFields(t *testing.T) {
	selections := []string{
		"", "name", "name,id", "sequence_id,name,id", "name,sequence_id,priority", "name, id",
		"name,state,priority,labels,assignees", "name,labels", "name,description_html",
		"name, state, priority, labels, assignees",
	}
	for _, c := range filterCases {
		for _, fields := range selections {
			t.Run(c.name+"/"+fields, func(t *testing.T) {
				f := &fakePlane{t: t}
				args := append([]string{}, c.flags...)
				if fields != "" {
					args = append(args, "--fields", fields)
				}
				res, env := listIssues(t, f, args...)
				if res.code != 0 || env["ok"] != true {
					t.Fatalf("exit %d: %s", res.code, res.stdout)
				}
				if got := names(t, env, "name"); !reflect.DeepEqual(got, c.want) {
					t.Fatalf("membership with --fields %q = %v, want %v", fields, got, c.want)
				}
				filtered := env["meta"].(map[string]any)["filtered"].(map[string]any)
				if filtered["before"] != float64(len(corpus)) || filtered["after"] != float64(len(c.want)) || filtered["client_side"] != true {
					t.Errorf("meta.filtered = %v", filtered)
				}
				if fields == "" {
					return
				}
				// Exactly the requested fields come out: nothing the filter needed leaks into the output.
				want := splitTrim(fields)
				slices.Sort(want)
				data, _ := env["data"].([]any)
				for _, row := range data {
					if got := keysOf(row); !reflect.DeepEqual(got, want) {
						t.Fatalf("output fields = %v, want %v", got, want)
					}
				}
			})
		}
	}
}

func splitTrim(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// The request asks the server for what the filters read, and for nothing else.
func TestIssueFilterRequestsOnlyTheFieldsItNeeds(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		query string // expected fields param, "" when absent
	}{
		{"no filter keeps the selection as typed", []string{"--fields", "id, name"}, "id, name"},
		{"no fields sends none", []string{"--state", "Backlog", "--priority", "high"}, ""},
		{"missing state added", []string{"--state", "Backlog", "--fields", "id,name"}, "id,name,state"},
		{"all four added in a fixed order", []string{"--assignee", userOne, "--label", "bug", "--priority", "high", "--state", "Backlog", "--fields", "name"}, "name,priority,state,labels,assignees"},
		{"present fields are not duplicated or reordered", []string{"--state", "Backlog", "--priority", "high", "--fields", "state,id,priority"}, "state,id,priority"},
		{"only the missing one is added", []string{"--state", "Backlog", "--priority", "high", "--fields", "id,priority"}, "id,priority,state"},
		{"spaces normalized when adding", []string{"--state", "Backlog", "--fields", " id , name "}, "id,name,state"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakePlane{t: t}
			if res, _ := listIssues(t, f, c.args...); res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stdout)
			}
			if len(f.requests) != 1 {
				t.Fatalf("work-item requests: %d", len(f.requests))
			}
			if got := f.requests[0].Get("fields"); got != c.query {
				t.Fatalf("server asked for fields %q, want %q", got, c.query)
			}
		})
	}
}

func TestIssueFilterKeepsServerKeyOrderAndExactValues(t *testing.T) {
	f := &fakePlane{t: t}
	res, env := listIssues(t, f, "--state", "Backlog", "--fields", "sequence_id,point,name,id")
	if res.code != 0 {
		t.Fatal(res.stdout)
	}
	first := res.stdout[strings.Index(res.stdout, `"data":[`):]
	first = first[:strings.Index(first, "}")]
	positions := []int{strings.Index(first, `"sequence_id"`), strings.Index(first, `"point":1.50`), strings.Index(first, `"name"`), strings.Index(first, `"id"`)}
	if !slices.IsSorted(positions) || slices.Contains(positions, -1) {
		t.Fatalf("key order or raw value lost: %s", first)
	}
	if got := names(t, env, "name"); len(got) != 4 {
		t.Fatalf("membership: %v", got)
	}
}

func TestIssueFilterWorksOnExpandedRelations(t *testing.T) {
	for _, c := range filterCases {
		for _, fields := range []string{"", "name", "name,state,labels"} {
			t.Run(c.name+"/"+fields, func(t *testing.T) {
				f := &fakePlane{t: t}
				args := append([]string{"--expand", "state,labels,assignees"}, c.flags...)
				if fields != "" {
					args = append(args, "--fields", fields)
				}
				res, env := listIssues(t, f, args...)
				if res.code != 0 {
					t.Fatal(res.stdout)
				}
				if got := names(t, env, "name"); !reflect.DeepEqual(got, c.want) {
					t.Fatalf("expanded membership = %v, want %v", got, c.want)
				}
				if f.requests[0].Get("expand") != "state,labels,assignees" {
					t.Errorf("expand was not forwarded: %v", f.requests[0])
				}
				if fields == "name,state,labels" && len(c.want) > 0 {
					// A requested expanded relation is returned as the server shaped it.
					row := env["data"].([]any)[0].(map[string]any)
					if _, ok := row["state"].(map[string]any); !ok {
						t.Errorf("expanded state was flattened: %v", row["state"])
					}
				}
			})
		}
	}
}

// A filter that cannot read a record must fail loudly, not report an empty list.
func TestIssueFilterFailsWhenTheServerOmitsAFilterField(t *testing.T) {
	for _, c := range []struct {
		omit  string
		flags []string
	}{
		{"state", []string{"--state", "Backlog"}},
		{"priority", []string{"--priority", "high"}},
		{"labels", []string{"--label", "bug"}},
		{"assignees", []string{"--assignee", userOne}},
	} {
		t.Run(c.omit, func(t *testing.T) {
			f := &fakePlane{t: t, omit: map[string]bool{c.omit: true}}
			res, env := listIssues(t, f, append(c.flags, "--fields", "name")...)
			if res.code == 0 || env["ok"] != false {
				t.Fatalf("a filter over a missing field succeeded: exit %d %s", res.code, res.stdout)
			}
			message, _ := env["error"].(map[string]any)["message"].(string)
			if !strings.Contains(message, `"`+c.omit+`"`) {
				t.Errorf("error does not name the missing field: %q", message)
			}
			if _, has := env["data"]; has {
				t.Errorf("a failure carried data: %v", env)
			}
		})
	}
}

func TestIssueFilterKeepsPaginationAndFilterMetadata(t *testing.T) {
	f := &fakePlane{t: t, pageSize: 2}
	res, env := listIssues(t, f, "--state", "Backlog", "--fields", "name,id")
	if res.code != 0 {
		t.Fatal(res.stdout)
	}
	if got := names(t, env, "name"); !reflect.DeepEqual(got, []string{"issue-1", "issue-2", "issue-5", "issue-6"}) {
		t.Fatalf("membership across pages: %v", got)
	}
	meta := env["meta"].(map[string]any)
	pagination := meta["pagination"].(map[string]any)
	if pagination["pages"] != 3.0 || pagination["fetched"] != 6.0 || pagination["drained"] != true || pagination["total_count"] != 6.0 {
		t.Errorf("pagination = %v", pagination)
	}
	if filtered := meta["filtered"].(map[string]any); filtered["before"] != 6.0 || filtered["after"] != 4.0 {
		t.Errorf("filtered = %v", filtered)
	}
	if len(f.requests) != 3 {
		t.Fatalf("requests: %d", len(f.requests))
	}
	for _, q := range f.requests {
		if q.Get("fields") != "name,id,state" {
			t.Errorf("a page was fetched with fields %q", q.Get("fields"))
		}
	}
}

func TestIssueFilterWithoutMatchesIsAnEmptySuccess(t *testing.T) {
	f := &fakePlane{t: t}
	res, env := listIssues(t, f, "--priority", "urgent", "--fields", "name")
	if res.code != 0 || env["ok"] != true || env["data"] != nil {
		t.Fatalf("no match: exit %d %s", res.code, res.stdout)
	}
	if filtered := env["meta"].(map[string]any)["filtered"].(map[string]any); filtered["before"] != 6.0 || filtered["after"] != 0.0 {
		t.Errorf("filtered = %v", filtered)
	}
}

func TestIssueUnfilteredFieldsAreUntouched(t *testing.T) {
	f := &fakePlane{t: t}
	res, env := listIssues(t, f, "--fields", "name")
	if res.code != 0 {
		t.Fatal(res.stdout)
	}
	if got := names(t, env, "name"); len(got) != len(corpus) {
		t.Fatalf("unfiltered rows: %v", got)
	}
	if _, has := env["meta"].(map[string]any)["filtered"]; has {
		t.Error("an unfiltered list reported meta.filtered")
	}
	for _, row := range env["data"].([]any) {
		if got := keysOf(row); !reflect.DeepEqual(got, []string{"name"}) {
			t.Fatalf("fields = %v", got)
		}
	}
}
