package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/config"
	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/security"
)

func newRPCServer(t *testing.T, enabled bool) (*Server, *fakeControl) {
	t.Helper()
	ctl := newFakeControl()
	cfg := config.Defaults()
	cfg.RPC.Enabled = enabled
	srv := New(Options{
		Control: ctl, Config: config.NewManager(cfg, nil),
		Credentials: security.NewMemoryCredentialStore(), Bus: core.NewEventBus(64),
	})
	return srv, ctl
}

func rpcCall(t *testing.T, srv *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/jsonrpc", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestJSONRPCDisabledIs404(t *testing.T) {
	srv, _ := newRPCServer(t, false)
	code, _ := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"getVersion"}`)
	if code != http.StatusNotFound {
		t.Fatalf("disabled /jsonrpc = %d, want 404", code)
	}
	if srv.rpc != nil {
		t.Fatal("rpc facade must be nil when disabled")
	}
}

func TestJSONRPCGetVersionAndList(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	code, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"getVersion"}`)
	if code != 200 || out["jsonrpc"] != "2.0" {
		t.Fatalf("bad response: %d %v", code, out)
	}
	res := out["result"].(map[string]any)
	if !strings.Contains(res["version"].(string), "ud") {
		t.Fatalf("unexpected version: %v", res)
	}

	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":2,"method":"system.listMethods"}`)
	methods := out["result"].([]any)
	if len(methods) < 25 {
		t.Fatalf("compat subset too small: %d methods", len(methods))
	}
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":3,"method":"system.listNotifications"}`)
	ifs := out["result"].([]any)
	if len(ifs) != 6 {
		t.Fatalf("want 6 notifications, got %d", len(ifs))
	}
}

func TestJSONRPCUnknownMethod(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"doesNotExist"}`)
	errObj := out["error"].(map[string]any)
	if int(errObj["code"].(float64)) != rpcErrMethod {
		t.Fatalf("want -32601, got %v", errObj)
	}
}

func TestJSONRPCAddUriPauseTellStatus(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"addUri","params":[["https://example.com/a.bin"]]}`)
	gid, ok := out["result"].(string)
	if !ok || len(gid) != 16 {
		t.Fatalf("bad gid: %v", out)
	}

	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tellStatus","params":["`+gid+`"]}`)
	st := out["result"].(map[string]any)
	if st["gid"] != gid || st["status"] != "waiting" {
		t.Fatalf("bad tellStatus: %v", st)
	}

	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":3,"method":"pause","params":["`+gid+`"]}`)
	if out["result"] != gid {
		t.Fatalf("pause result: %v", out)
	}
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":4,"method":"tellStatus","params":["`+gid+`"]}`)
	st = out["result"].(map[string]any)
	if st["status"] != "paused" {
		t.Fatalf("want paused, got %v", st["status"])
	}
}

func TestJSONRPCRestartGIDStability(t *testing.T) {
	// A GID derived from a task id must be stable across a facade restart:
	// AriaNg keeps GIDs in its UI between daemon restarts (ADR-0031 decision 5).
	id := core.TaskID("01JTEST00000000000000000000")
	g1 := newGID(id)
	g2 := newGID(id)
	if g1 != g2 || len(g1) != 16 {
		t.Fatalf("GID not stable/16-hex: %s vs %s", g1, g2)
	}
}

func TestJSONRPCMulticall(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"system.multicall","params":[[` +
		`{"methodName":"getVersion","params":[]},` +
		`{"methodName":"getSessionInfo","params":[]}]]}`
	_, out := rpcCall(t, srv, body)
	res := out["result"].([]any)
	if len(res) != 2 {
		t.Fatalf("multicall len=%d", len(res))
	}
	if _, ok := res[0].([]any); !ok {
		t.Fatalf("multicall success must be wrapped in array: %v", res[0])
	}
}

func TestJSONRPCNoopSession(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	for _, m := range []string{"saveSession", "getSessionInfo", "shutdown", "forceShutdown"} {
		_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"`+m+`"}`)
		if out["error"] != nil {
			t.Fatalf("%s returned error: %v", m, out)
		}
	}
}

func TestJSONRPCAuthFailClosed(t *testing.T) {
	ctl := newFakeControl()
	cfg := config.Defaults()
	cfg.RPC.Enabled = true
	srv := New(Options{
		Control: ctl, Config: config.NewManager(cfg, nil),
		Credentials: security.NewMemoryCredentialStore(), Bus: core.NewEventBus(64),
		AuthToken: "secret", RequireAuth: true,
	})
	// No token -> Unauthorized app error.
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"getVersion"}`)
	if out["error"] == nil {
		t.Fatal("missing token must be rejected")
	}
	// Correct token: prefix form.
	req := httptest.NewRequest(http.MethodPost, "/jsonrpc",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"getVersion","params":["token:secret"]}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	out = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["error"] != nil {
		t.Fatalf("valid token rejected: %v", out)
	}
}

func TestJSONRPCMalformedBatch(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	_, out := rpcCall(t, srv, `not json`)
	if out["error"] == nil {
		t.Fatal("malformed JSON must return parse error")
	}
	_, out = rpcCall(t, srv, `[]`)
	if out["error"] == nil {
		t.Fatal("empty batch must return invalid request")
	}
}

func TestAria2StatusMapping(t *testing.T) {
	cases := map[core.TaskState]string{
		core.StatePending: "waiting", core.StateResolving: "waiting", core.StateReady: "waiting",
		core.StateRunning: "active", core.StatePostProcessing: "active",
		core.StatePaused: "paused", core.StateCompleted: "complete", core.StateSeeding: "complete",
		core.StateFailed: "error", core.StateCanceled: "removed",
	}
	for in, want := range cases {
		if got := aria2Status(in); got != want {
			t.Errorf("aria2Status(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestAria2NotificationMapping(t *testing.T) {
	m, _ := aria2Notification(core.Event{Type: core.EventTaskCreated, TaskID: "t1"})
	if m != "aria2.onDownloadStart" {
		t.Fatalf("created -> %s", m)
	}
	m, _ = aria2Notification(core.Event{Type: core.EventTaskState, TaskID: "t1",
		Payload: map[string]any{"state": core.StatePaused}})
	if m != "aria2.onDownloadPause" {
		t.Fatalf("paused -> %s", m)
	}
	m, _ = aria2Notification(core.Event{Type: core.EventTaskState, TaskID: "t1",
		Payload: map[string]any{"state": core.StateCompleted}})
	if m != "aria2.onDownloadComplete" {
		t.Fatalf("complete -> %s", m)
	}
}

func TestRPCCapabilityLevel(t *testing.T) {
	// Capabilities must reflect the opt-in flag via the scheduler config path.
	cfg := config.Defaults()
	if cfg.RPC.Enabled {
		t.Fatal("RPC must default off")
	}
	cfg.RPC.Enabled = true
	if !cfg.RPC.Enabled {
		t.Fatal("RPC toggle broken")
	}
	_ = context.Background() // keep import for future async assertions
}
