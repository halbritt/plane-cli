package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plane-cli/internal/cli"
)

// issueFixture mirrors the v1.3.1 IssueSerializer field list (mined from
// apps/api/plane/api/serializers/issue.py at tag v1.3.1).
func issueFixture(id, project, name string) map[string]any {
	return map[string]any{
		"id": id, "created_at": "2026-07-23T10:00:00.000000Z",
		"updated_at": "2026-07-23T10:00:00.000000Z",
		"created_by": "a3f369ea-d8dd-42ed-bea3-db653abd632b",
		"updated_by": nil, "deleted_at": nil, "point": nil,
		"name": name, "description_html": "<p></p>", "description_binary": nil,
		"priority": "none", "start_date": nil, "target_date": nil,
		"sequence_id": 12, "sort_order": 65535.0, "completed_at": nil,
		"archived_at": nil, "is_draft": false,
		"external_source": nil, "external_id": nil, "parent": nil,
		"state": "st-uuid", "estimate_point": nil,
		"project": project, "workspace": "ws-uuid", "type_id": nil,
		"assignees": []string{}, "labels": []string{},
	}
}

// page wraps results in the v1.3.1 paginated envelope.
func page(results []any, nextCursor string, more bool, total int) map[string]any {
	return map[string]any{
		"grouped_by": nil, "sub_grouped_by": nil,
		"total_count": total, "next_cursor": nextCursor, "prev_cursor": "2:-1:1",
		"next_page_results": more, "prev_page_results": false,
		"count": len(results), "total_pages": (total + 1) / 2, "total_results": total,
		"extra_stats": nil, "results": results,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type runResult struct {
	code   int
	stdout string
	stderr string
}

func run(t *testing.T, baseURL string, extraEnv map[string]string, stdin string, args ...string) runResult {
	t.Helper()
	env := map[string]string{
		"PLANE_BASE_URL":  baseURL,
		"PLANE_API_KEY":   "test-key",
		"PLANE_WORKSPACE": "ws",
		"PLANE_CONFIG":    filepath.Join(t.TempDir(), "absent.toml"),
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	getenv := func(k string) string { return env[k] }

	var stdout, stderr bytes.Buffer
	code := mainWithStdin(t, strings.NewReader(stdin), &stdout, &stderr, getenv, args)
	return runResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// mainWithStdin mirrors cli.Main but with injectable stdin.
func mainWithStdin(t *testing.T, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, args []string) int {
	t.Helper()
	// cli.Main hardwires os.Stdin; swap it for bulk-mode tests.
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		io.Copy(w, stdin)
		w.Close()
	}()
	os.Stdin = r
	defer func() { os.Stdin = old }()
	return cli.Main(context.Background(), args, stdout, stderr, getenv)
}

func decodeEnvelope(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not a single JSON envelope: %v\nstdout: %q", err, stdout)
	}
	return env
}

func errField(t *testing.T, env map[string]any, key string) any {
	t.Helper()
	e, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope has no error object: %v", env)
	}
	return e[key]
}

func TestExitCodesAndErrorEnvelope(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       any
		wantCode   int
		wantErr    string
		wantStatus float64
	}{
		{"auth401", 401, map[string]any{"detail": "Given API token is not valid"}, 3, "auth", 401},
		{"forbidden403", 403, map[string]any{"detail": "You do not have permission to perform this action."}, 3, "auth", 403},
		{"notfound404", 404, map[string]any{"error": "The requested resource does not exist."}, 4, "not_found", 404},
		{"validation400", 400, map[string]any{"error": "Please provide valid detail"}, 5, "validation", 400},
		{"conflict409", 409, map[string]any{"error": "already exists", "id": "x"}, 5, "conflict", 409},
		{"server500", 500, map[string]any{"error": "Something went wrong please try again later"}, 6, "server", 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Api-Key") != "test-key" {
					t.Errorf("missing X-Api-Key header")
				}
				writeJSON(w, tc.status, tc.body)
			}))
			defer srv.Close()

			res := run(t, srv.URL, nil, "", "me", "--max-retries", "0")
			if res.code != tc.wantCode {
				t.Fatalf("exit = %d, want %d (stdout %s)", res.code, tc.wantCode, res.stdout)
			}
			env := decodeEnvelope(t, res.stdout)
			if env["ok"] != false {
				t.Errorf("ok = %v, want false", env["ok"])
			}
			if got := errField(t, env, "code"); got != tc.wantErr {
				t.Errorf("error.code = %v, want %v", got, tc.wantErr)
			}
			if got := errField(t, env, "http_status"); got != tc.wantStatus {
				t.Errorf("error.http_status = %v, want %v", got, tc.wantStatus)
			}
			if errField(t, env, "details") == nil {
				t.Errorf("error.details missing; server body should pass through")
			}
		})
	}
}

func TestRateLimitedExhaustedExit7(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		writeJSON(w, 429, map[string]any{"detail": "Request was throttled. Expected available in 1 second."})
	}))
	defer srv.Close()

	res := run(t, srv.URL, nil, "", "me", "--max-retries", "1")
	if res.code != 7 {
		t.Fatalf("exit = %d, want 7", res.code)
	}
	env := decodeEnvelope(t, res.stdout)
	if got := errField(t, env, "code"); got != "rate_limited" {
		t.Errorf("error.code = %v, want rate_limited", got)
	}
}

func TestSuccessPassthrough(t *testing.T) {
	user := map[string]any{"id": "u1", "first_name": "H", "last_name": "A",
		"email": "h@example.com", "avatar": "", "avatar_url": nil, "display_name": "h"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/me/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		writeJSON(w, 200, user)
	}))
	defer srv.Close()

	res := run(t, srv.URL, nil, "", "me")
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %s stderr %s)", res.code, res.stdout, res.stderr)
	}
	env := decodeEnvelope(t, res.stdout)
	if env["ok"] != true {
		t.Fatalf("ok = %v", env["ok"])
	}
	data, _ := json.Marshal(env["data"])
	want, _ := json.Marshal(user)
	if string(data) != string(want) {
		t.Errorf("data not passed through verbatim:\n got %s\nwant %s", data, want)
	}
}

func TestMissingConfigExit2(t *testing.T) {
	res := run(t, "", map[string]string{"PLANE_BASE_URL": "", "PLANE_API_KEY": ""}, "", "me")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2", res.code)
	}
	env := decodeEnvelope(t, res.stdout)
	if got := errField(t, env, "code"); got != "config" {
		t.Errorf("error.code = %v, want config", got)
	}
}

func TestUnknownFlagUsageEnvelope(t *testing.T) {
	res := run(t, "http://unused", nil, "", "me", "--bogus")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2", res.code)
	}
	env := decodeEnvelope(t, res.stdout)
	if got := errField(t, env, "code"); got != "usage" {
		t.Errorf("error.code = %v, want usage", got)
	}
}

func TestHelpGoesToStderrOnly(t *testing.T) {
	res := run(t, "http://unused", nil, "", "--help")
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0", res.code)
	}
	if res.stdout != "" {
		t.Errorf("stdout should be empty for --help, got %q", res.stdout)
	}
	if !strings.Contains(res.stderr, "Exit codes") {
		t.Errorf("help text missing from stderr")
	}
}

func TestPaginationDrain(t *testing.T) {
	// Three pages of two projects each.
	var gotPerPage, gotCursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws/projects/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotPerPage = append(gotPerPage, r.URL.Query().Get("per_page"))
		cursor := r.URL.Query().Get("cursor")
		gotCursors = append(gotCursors, cursor)
		mk := func(i int) any {
			return map[string]any{"id": fmt.Sprintf("p%d", i), "name": fmt.Sprintf("Proj %d", i), "identifier": fmt.Sprintf("P%d", i)}
		}
		switch cursor {
		case "1000:0:0":
			writeJSON(w, 200, page([]any{mk(1), mk(2)}, "1000:1:0", true, 6))
		case "1000:1:0":
			writeJSON(w, 200, page([]any{mk(3), mk(4)}, "1000:2:0", true, 6))
		case "1000:2:0":
			writeJSON(w, 200, page([]any{mk(5), mk(6)}, "1000:3:0", false, 6))
		default:
			t.Errorf("unexpected cursor %q", cursor)
			writeJSON(w, 400, map[string]any{"error": "Invalid cursor parameter."})
		}
	}))
	defer srv.Close()

	res := run(t, srv.URL, nil, "", "project", "list")
	if res.code != 0 {
		t.Fatalf("exit = %d (stdout %s)", res.code, res.stdout)
	}
	env := decodeEnvelope(t, res.stdout)
	data := env["data"].([]any)
	if len(data) != 6 {
		t.Errorf("drained %d results, want 6", len(data))
	}
	meta := env["meta"].(map[string]any)["pagination"].(map[string]any)
	if meta["pages"] != 3.0 || meta["drained"] != true {
		t.Errorf("pagination meta = %v", meta)
	}
	if len(gotCursors) != 3 {
		t.Errorf("server saw cursors %v", gotCursors)
	}
}

func TestPaginationLimitAndResume(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		if per := r.URL.Query().Get("per_page"); per != "3" {
			t.Errorf("per_page = %s, want 3 (min(limit, 1000))", per)
		}
		mk := func(i int) any {
			return map[string]any{"id": fmt.Sprintf("p%d", i), "name": fmt.Sprintf("Proj %d", i)}
		}
		switch cursor {
		case "3:0:0":
			writeJSON(w, 200, page([]any{mk(1), mk(2), mk(3)}, "3:1:0", true, 7))
		case "3:1:0":
			writeJSON(w, 200, page([]any{mk(4), mk(5), mk(6)}, "3:2:0", true, 7))
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
	}))
	defer srv.Close()

	res := run(t, srv.URL, nil, "", "project", "list", "--limit", "3")
	env := decodeEnvelope(t, res.stdout)
	if n := len(env["data"].([]any)); n != 3 {
		t.Fatalf("got %d results, want 3", n)
	}
	meta := env["meta"].(map[string]any)["pagination"].(map[string]any)
	if meta["drained"] != false || meta["next_cursor"] != "3:1:0" {
		t.Errorf("pagination meta = %v, want undrained with next_cursor 3:1:0", meta)
	}

	// Resume from the reported cursor.
	res = run(t, srv.URL, nil, "", "project", "list", "--limit", "3", "--cursor", "3:1:0")
	env = decodeEnvelope(t, res.stdout)
	first := env["data"].([]any)[0].(map[string]any)
	if first["id"] != "p4" {
		t.Errorf("resume started at %v, want p4", first["id"])
	}
}

func projectListHandler(t *testing.T, projects []map[string]any) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/v1/workspaces/ws/projects/" && r.Method == "GET" {
			results := make([]any, len(projects))
			for i, p := range projects {
				results[i] = p
			}
			writeJSON(w, 200, page(results, "1000:1:0", false, len(results)))
			return true
		}
		return false
	}
}

func TestNameResolutionOnCreate(t *testing.T) {
	projects := []map[string]any{
		{"id": "11111111-1111-4111-8111-111111111111", "name": "My Project", "identifier": "MYPROJ"},
		{"id": "22222222-2222-4222-8222-222222222222", "name": "Other", "identifier": "OTHER"},
	}
	var createdBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if projectListHandler(t, projects)(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws/projects/11111111-1111-4111-8111-111111111111/states/" && r.Method == "GET":
			writeJSON(w, 200, page([]any{
				map[string]any{"id": "33333333-3333-4333-8333-333333333333", "name": "Todo", "group": "unstarted"},
				map[string]any{"id": "44444444-4444-4444-8444-444444444444", "name": "Done", "group": "completed"},
			}, "1000:1:0", false, 2))
		case r.URL.Path == "/api/v1/workspaces/ws/projects/11111111-1111-4111-8111-111111111111/work-items/" && r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&createdBody)
			writeJSON(w, 201, issueFixture("i1", "11111111-1111-4111-8111-111111111111", "X"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			writeJSON(w, 404, map[string]any{"error": "The requested resource does not exist."})
		}
	}))
	defer srv.Close()

	res := run(t, srv.URL, nil, "", "issue", "create", "-p", "myproj", "--name", "X", "--state", "todo")
	if res.code != 0 {
		t.Fatalf("exit = %d (stdout %s stderr %s)", res.code, res.stdout, res.stderr)
	}
	if createdBody["state"] != "33333333-3333-4333-8333-333333333333" {
		t.Errorf("state sent = %v, want resolved Todo uuid", createdBody["state"])
	}
	if createdBody["name"] != "X" {
		t.Errorf("name sent = %v", createdBody["name"])
	}
	env := decodeEnvelope(t, res.stdout)
	resolved := env["meta"].(map[string]any)["resolved"].(map[string]any)
	if resolved["project"] == nil || resolved["state"] == nil {
		t.Errorf("meta.resolved incomplete: %v", resolved)
	}
}

func TestAmbiguousProjectResolution(t *testing.T) {
	projects := []map[string]any{
		{"id": "11111111-1111-4111-8111-111111111111", "name": "Dup", "identifier": "D1"},
		{"id": "22222222-2222-4222-8222-222222222222", "name": "dup", "identifier": "D2"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !projectListHandler(t, projects)(w, r) {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	res := run(t, srv.URL, nil, "", "issue", "list", "-p", "dup")
	if res.code != 5 {
		t.Fatalf("exit = %d, want 5 (stdout %s)", res.code, res.stdout)
	}
	env := decodeEnvelope(t, res.stdout)
	if got := errField(t, env, "code"); got != "ambiguous" {
		t.Errorf("error.code = %v, want ambiguous", got)
	}
	details := errField(t, env, "details").(map[string]any)
	if n := len(details["candidates"].([]any)); n != 2 {
		t.Errorf("candidates = %d, want 2", n)
	}
}

func TestNoResolveRequiresUUID(t *testing.T) {
	res := run(t, "http://unused", nil, "", "issue", "list", "-p", "myproj", "--no-resolve")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %s)", res.code, res.stdout)
	}
}

func TestIssueGetByIdentifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws/work-items/MYPROJ-12/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		writeJSON(w, 200, issueFixture("i12", "p1", "The bug"))
	}))
	defer srv.Close()

	res := run(t, srv.URL, nil, "", "issue", "get", "myproj-12")
	if res.code != 0 {
		t.Fatalf("exit = %d (stdout %s)", res.code, res.stdout)
	}
	env := decodeEnvelope(t, res.stdout)
	if env["data"].(map[string]any)["sequence_id"] != 12.0 {
		t.Errorf("unexpected data: %v", env["data"])
	}
}

func TestBulkCreatePartialFailure(t *testing.T) {
	projects := []map[string]any{{"id": "11111111-1111-4111-8111-111111111111", "name": "P", "identifier": "P"}}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if projectListHandler(t, projects)(w, r) {
			return
		}
		if r.Method == "POST" {
			n++
			if n == 2 {
				writeJSON(w, 400, map[string]any{"name": []string{"This field may not be blank."}})
				return
			}
			writeJSON(w, 201, issueFixture(fmt.Sprintf("i%d", n), "p1", "bulk"))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	stdin := `{"name":"one"}` + "\n" + `{"name":""}` + "\n"
	res := run(t, srv.URL, nil, stdin, "issue", "create", "-p", "P", "-")
	if res.code != 5 {
		t.Fatalf("exit = %d, want 5 (stdout %s)", res.code, res.stdout)
	}
	env := decodeEnvelope(t, res.stdout)
	details := errField(t, env, "details").(map[string]any)
	results := details["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].(map[string]any)["ok"] != true || results[1].(map[string]any)["ok"] != false {
		t.Errorf("per-item outcomes wrong: %v", results)
	}
}

func TestAttachmentUploadFlow(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("hello attachment"), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		presignBody   map[string]any
		storageAuth   string
		storageFields = map[string]string{}
		storageFile   []byte
		confirmed     bool
	)
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws/work-items/MYPROJ-12/" && r.Method == "GET":
			fx := issueFixture("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "target")
			writeJSON(w, 200, fx)
		case strings.HasSuffix(r.URL.Path, "/attachments/") && r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&presignBody)
			writeJSON(w, 200, map[string]any{
				"upload_data": map[string]any{
					"url": srvURL + "/storage/uploads",
					"fields": map[string]string{
						"key":          "ws-uuid/abc-note.txt",
						"Content-Type": "text/plain",
						"policy":       "cG9saWN5",
					},
				},
				"asset_id":   "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
				"attachment": map[string]any{"id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "is_uploaded": false},
				"asset_url":  "/api/assets/v2/...",
			})
		case r.URL.Path == "/storage/uploads" && r.Method == "POST":
			storageAuth = r.Header.Get("X-Api-Key")
			mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mt != "multipart/form-data" {
				t.Errorf("storage content-type = %s", mt)
			}
			mr := multipartReader(t, r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(part)
				if part.FormName() == "file" {
					storageFile = b
				} else {
					storageFields[part.FormName()] = string(b)
				}
			}
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/attachments/cccccccc-cccc-4ccc-8ccc-cccccccccccc/") && r.Method == "PATCH":
			confirmed = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	res := run(t, srv.URL, nil, "", "attachment", "upload", "MYPROJ-12", "--file", file)
	if res.code != 0 {
		t.Fatalf("exit = %d (stdout %s stderr %s)", res.code, res.stdout, res.stderr)
	}
	if presignBody["name"] != "note.txt" || presignBody["size"] != 16.0 || presignBody["type"] != "text/plain" {
		t.Errorf("presign body = %v", presignBody)
	}
	if storageAuth != "" {
		t.Errorf("API key leaked to storage host")
	}
	if storageFields["key"] != "ws-uuid/abc-note.txt" || storageFields["policy"] != "cG9saWN5" {
		t.Errorf("policy fields not forwarded: %v", storageFields)
	}
	if string(storageFile) != "hello attachment" {
		t.Errorf("file bytes = %q", storageFile)
	}
	if !confirmed {
		t.Errorf("upload was not confirmed via PATCH")
	}
	env := decodeEnvelope(t, res.stdout)
	if env["data"].(map[string]any)["uploaded"] != true {
		t.Errorf("data.uploaded = %v", env["data"])
	}
}

func multipartReader(t *testing.T, body io.Reader, boundary string) *multipart.Reader {
	t.Helper()
	if boundary == "" {
		t.Fatal("no multipart boundary")
	}
	return multipart.NewReader(body, boundary)
}
