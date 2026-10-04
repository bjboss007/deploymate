package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI is a stand-in dashboard that records what the MCP server asked of it.
type fakeAPI struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []string // "METHOD path?query body"
	scope string
}

func newFakeAPI(t *testing.T, scope string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{scope: scope}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dm_test" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid or expired token"}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/whoami":
			io.WriteString(w, `{"user":"o@x","scope":"`+f.scope+`"}`)
		case strings.HasSuffix(r.URL.Path, "/nope"):
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"no such app"}`)
		default:
			io.WriteString(w, `{"ok":true,"path":"`+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

// rpc runs the given JSON-RPC lines through a server and returns each reply.
func rpc(t *testing.T, f *fakeAPI, token string, lines ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	s := New(f.srv.URL, token, "test", io.Discard)
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("stdout carried a non-JSON line %q", sc.Text())
		}
		replies = append(replies, m)
	}
	return replies
}

func toolNames(reply map[string]any) map[string]bool {
	names := map[string]bool{}
	for _, tl := range reply["result"].(map[string]any)["tools"].([]any) {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	return names
}

func TestInitializeAndToolListFollowTheTokensScope(t *testing.T) {
	f := newFakeAPI(t, "read")
	rep := rpc(t, f, "dm_test",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"nope/nope"}`,
	)
	if len(rep) != 4 { // the notification gets no reply
		t.Fatalf("got %d replies, want 4: %v", len(rep), rep)
	}
	init := rep[0]["result"].(map[string]any)
	if init["protocolVersion"] == "" || init["serverInfo"].(map[string]any)["name"] != "deploymate" {
		t.Errorf("initialize = %v", init)
	}
	names := toolNames(rep[1])
	for _, want := range []string{"fleet_status", "get_app", "get_deployment_log", "get_service"} {
		if !names[want] {
			t.Errorf("a read token should see %q", want)
		}
	}
	for _, tl := range rep[1]["result"].(map[string]any)["tools"].([]any) {
		ann := tl.(map[string]any)["annotations"].(map[string]any)
		if ann["readOnlyHint"] != true {
			t.Errorf("%v is not marked read-only", tl.(map[string]any)["name"])
		}
	}
	if rep[3]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("unknown method = %v", rep[3])
	}
}

func TestToolCallsBecomeAPIRequests(t *testing.T) {
	f := newFakeAPI(t, "read")
	call := func(name, argsJSON string) map[string]any {
		rep := rpc(t, f, "dm_test", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+argsJSON+`}}`)
		return rep[0]["result"].(map[string]any)
	}
	res := call("get_app", `{"app":"erp"}`)
	if res["isError"] == true || f.last() != "GET /api/v1/apps/erp " {
		t.Errorf("get_app: %v / %q", res, f.last())
	}
	call("list_deployments", `{"app":"erp","limit":5}`)
	if f.last() != "GET /api/v1/apps/erp/deployments?limit=5 " {
		t.Errorf("list_deployments sent %q", f.last())
	}
	// Names are path-escaped: no way to climb out of the path.
	call("get_app", `{"app":"../fleet"}`)
	if !strings.Contains(f.last(), "/apps/..%2Ffleet") {
		t.Errorf("an app name escaped its path segment: %q", f.last())
	}
	// A missing argument is a tool error that makes no request.
	nonWhoami := func() int {
		n := 0
		for _, c := range f.calls {
			if !strings.Contains(c, "/whoami") {
				n++
			}
		}
		return n
	}
	n := nonWhoami()
	res = call("get_app", `{}`)
	if res["isError"] != true || nonWhoami() != n {
		t.Errorf("missing arg: %v, calls %d→%d", res, n, nonWhoami())
	}
	// An API error becomes a tool error carrying the API's message.
	res = call("get_app", `{"app":"nope"}`)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, "404") || !strings.Contains(text, "no such app") {
		t.Errorf("404 = %v", res)
	}
}

func TestBadTokenAndUnknownToolsAreErrors(t *testing.T) {
	f := newFakeAPI(t, "read")
	rep := rpc(t, f, "dm_wrong", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fleet_status","arguments":{}}}`)
	res := rep[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("a rejected token must surface as a tool error: %v", res)
	}
	rep = rpc(t, f, "dm_test", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_everything","arguments":{}}}`)
	if rep[0]["result"].(map[string]any)["isError"] != true {
		t.Error("an unknown tool must be an error")
	}
}

func TestLongResultsAreTruncated(t *testing.T) {
	res := toolText(strings.Repeat("x", maxResultBytes+500), false)
	text := res["content"].([]map[string]string)[0]["text"]
	if len(text) > maxResultBytes+30 || !strings.HasSuffix(text, "(truncated)") {
		t.Errorf("result length %d, tail %q", len(text), text[len(text)-20:])
	}
}
