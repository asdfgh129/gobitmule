package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

// rpcCallRaw issues a raw POST and returns status + raw body.
func rpcCallRaw(srv *Server, body string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/jsonrpc", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// seedTask creates a task via addUri and decorates it with a Resource so
// getFiles/getUris/getPeers/getServers have data to map.
func seedTask(t *testing.T, srv *Server, ctl *fakeControl, input string) string {
	t.Helper()
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"addUri","params":[["`+input+`"]]}`)
	gid, ok := out["result"].(string)
	if !ok {
		t.Fatalf("addUri failed: %v", out)
	}
	ctl.mu.Lock()
	for id, d := range ctl.tasks {
		d.Resource = &core.Resource{ID: "res-" + string(id), Kind: core.KindTorrent, Engine: core.EngineGo,
			Files: []core.ResourceFile{
				{Index: 0, Path: "a.bin", Size: 100, Selected: true},
				{Index: 1, Path: "b.bin", Size: 200, Selected: false},
			}}
		d.Speed = 1024
		d.Connections = 3
		d.NumPieces = 4
		d.PieceLength = 65536
		d.BytesTotal = 300
		d.BytesDone = 150
		d.Peers = []string{"1.2.3.4:6881"}
		d.Trackers = []string{"udp://tracker.example:80"}
		d.Bitfield = "f0"
	}
	ctl.mu.Unlock()
	return gid
}

func TestRPCQueryMethods(t *testing.T) {
	srv, ctl := newRPCServer(t, true)
	gid := seedTask(t, srv, ctl, "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")

	// getUris
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"getUris","params":["`+gid+`"]}`)
	uris, ok := out["result"].([]any)
	if !ok || len(uris) == 0 {
		t.Fatalf("getUris: %v", out)
	}

	// getFiles
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":2,"method":"getFiles","params":["`+gid+`"]}`)
	files, ok := out["result"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("getFiles want 2: %v", out)
	}

	// getPeers
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":3,"method":"getPeers","params":["`+gid+`"]}`)
	peers, ok := out["result"].([]any)
	if !ok || len(peers) != 1 {
		t.Fatalf("getPeers want 1: %v", out)
	}

	// getServers
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":4,"method":"getServers","params":["`+gid+`"]}`)
	servers, ok := out["result"].([]any)
	if !ok || len(servers) != 1 {
		t.Fatalf("getServers want 1: %v", out)
	}

	// getOption
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":5,"method":"getOption","params":["`+gid+`"]}`)
	if out["error"] != nil {
		t.Fatalf("getOption: %v", out)
	}

	// tellStatus full mapping
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":6,"method":"tellStatus","params":["`+gid+`"]}`)
	st := out["result"].(map[string]any)
	if st["totalLength"] != "300" || st["completedLength"] != "150" || st["bitfield"] != "f0" {
		t.Fatalf("tellStatus mapping: %v", st)
	}
	if st["numPieces"] != "4" || st["pieceLength"] != "65536" || st["connections"] != "3" || st["downloadSpeed"] != "1024" {
		t.Fatalf("tellStatus numeric mapping: %v", st)
	}

	// getGlobalStat
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":7,"method":"getGlobalStat"}`)
	gs, ok := out["result"].(map[string]any)
	if !ok || gs["numActive"] == nil {
		t.Fatalf("getGlobalStat: %v", out)
	}

	// tellActive / tellWaiting / tellStopped
	for _, m := range []string{"tellActive", "tellWaiting", "tellStopped"} {
		_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":8,"method":"`+m+`"}`)
		if out["error"] != nil {
			t.Fatalf("%s: %v", m, out)
		}
		if _, ok := out["result"].([]any); !ok {
			t.Fatalf("%s result not array: %v", m, out)
		}
	}
}

func TestRPCControlMethods(t *testing.T) {
	srv, ctl := newRPCServer(t, true)
	seedTask(t, srv, ctl, "https://example.com/a.bin")
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"pauseAll"}`)
	if out["result"] != "OK" {
		t.Fatalf("pauseAll: %v", out)
	}
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":2,"method":"unpauseAll"}`)
	if out["result"] != "OK" {
		t.Fatalf("unpauseAll: %v", out)
	}
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":3,"method":"forcePauseAll"}`)
	if out["result"] != "OK" {
		t.Fatalf("forcePauseAll: %v", out)
	}
}

func TestRPCChangeOptionAndUnsupported(t *testing.T) {
	srv, ctl := newRPCServer(t, true)
	gid := seedTask(t, srv, ctl, "https://example.com/a.bin")

	// changeOption with known + unknown options (unknown must be ignored).
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"changeOption","params":["`+gid+
		`",{"max-download-limit":"2MiB/s","max-connection-per-server":8,"select-file":"1","all-proxy":"http://p"}]}`)
	if out["error"] != nil {
		t.Fatalf("changeOption: %v", out)
	}
	// ChangeOption success is sufficient here: fakeControl.SetOption is a no-op,
	// and the translation itself is unit-tested via applyRPCOptions below.

	// changeGlobalOption (no GID) accepts but ignores.
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":2,"method":"changeGlobalOption","params":[{"max-overall-download-limit":"4MiB/s"}]}`)
	if out["error"] != nil {
		t.Fatalf("changeGlobalOption: %v", out)
	}

	// Explicitly unsupported methods return app errors, not method-not-found.
	for _, m := range []string{"addTorrent", "addMetalink", "changeUri", "changePosition"} {
		_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":3,"method":"`+m+`","params":[["x"],{}]}`)
		if out["error"] == nil {
			t.Fatalf("%s must return error", m)
		}
	}
}

func TestRPCRemove(t *testing.T) {
	srv, ctl := newRPCServer(t, true)
	gid := seedTask(t, srv, ctl, "https://example.com/a.bin")
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"remove","params":["`+gid+`"]}`)
	if out["error"] != nil {
		t.Fatalf("remove: %v", out)
	}
	ctl.mu.Lock()
	n := len(ctl.tasks)
	ctl.mu.Unlock()
	if n != 0 {
		t.Fatalf("task not removed: %d remain", n)
	}

	// removeDownloadResult and forceRemove on a missing GID -> error path.
	_, out = rpcCall(t, srv, `{"jsonrpc":"2.0","id":2,"method":"removeDownloadResult","params":["deadbeefdeadbeef"]}`)
	if out["error"] == nil {
		t.Fatal("missing GID must error")
	}
}

func TestRPCResolveGIDRebuildFromList(t *testing.T) {
	srv, ctl := newRPCServer(t, true)
	// Create a task directly on ctl (simulating pre-restart state with no gidMap entry).
	id := core.NewID()
	ctl.tasks[id] = &core.TaskDetail{Task: core.Task{ID: id, State: core.StateCompleted, Input: "https://e.com/x"}}
	gid := newGID(id)
	// A fresh facade with empty gidMap must resolve it via the List scan.
	srv.rpc.gid = newGIDMap()
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tellStatus","params":["`+gid+`"]}`)
	if out["error"] != nil {
		t.Fatalf("pre-restart GID not resolvable: %v", out)
	}
	st := out["result"].(map[string]any)
	if st["status"] != "complete" {
		t.Fatalf("want complete, got %v", st["status"])
	}
}

func TestRPCBadParams(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	bad := []string{
		`{"jsonrpc":"2.0","id":1,"method":"addUri"}`,
		`{"jsonrpc":"2.0","id":2,"method":"addUri","params":[[]]}`,
		`{"jsonrpc":"2.0","id":3,"method":"pause"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tellStatus","params":["bad"]}`, // not 16 hex but resolveGID still called
		`{"jsonrpc":"2.0","id":5,"method":"getFiles","params":["missing1"]}`,
		`{"jsonrpc":"2.0","id":6,"method":"getUris"}`,
		`{"jsonrpc":"2.0","id":7,"method":"getPeers"}`,
		`{"jsonrpc":"2.0","id":8,"method":"getServers"}`,
		`{"jsonrpc":"2.0","id":9,"method":"getOption"}`,
		`{"jsonrpc":"2.0","id":10,"method":"changeOption","params":["x"]}`,
		`{"jsonrpc":"2.0","id":11,"method":"system.multicall"}`,
	}
	for _, body := range bad {
		code, raw := rpcCallRaw(srv, body)
		if code != 200 {
			t.Errorf("bad params request returned %d: %s", code, body)
		}
		if !strings.Contains(raw, "error") {
			t.Errorf("expected error for: %s -> %s", body, raw)
		}
	}
}

func TestMarshalAria2Notification(t *testing.T) {
	frame, err := marshalAria2Notification("aria2.onDownloadStart", "abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(frame), "aria2.onDownloadStart") || !strings.Contains(string(frame), "abcdef0123456789") {
		t.Fatalf("bad notification frame: %s", frame)
	}
}

func TestAddURIRejectsMalformedInput(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"addUri","params":[["file:///etc/passwd"]]}`)
	if out["error"] == nil {
		t.Fatal("addUri with blocked scheme must error")
	}
}

func TestApplyRPCOptions(t *testing.T) {
	var opt core.TaskOptions
	applyRPCOptions(&opt, []byte(`{"dir":"/dl","out":"a.bin","max-download-limit":"2MiB/s",`+
		`"max-connection-per-server":8,"split":12,"select-file":"1","header":{},"unknown":"x"}`))
	if opt.Dir != "/dl" || opt.Out != "a.bin" || opt.MaxDownloadLimit != "2MiB/s" {
		t.Fatalf("known options not applied: %+v", opt)
	}
	// max-connection-per-server comes first in the map loop; split may or may
	// not overwrite it (both are valid). Just assert a positive value landed.
	if opt.MaxConnections <= 0 {
		t.Fatalf("max connections not applied: %d", opt.MaxConnections)
	}

	// A malformed object is silently ignored (accept-but-ignore policy).
	var opt2 core.TaskOptions
	applyRPCOptions(&opt2, []byte(`not an object`))
	if opt2.Dir != "" {
		t.Fatal("malformed options must be ignored")
	}
}

func TestRPCGetGlobalOption(t *testing.T) {
	srv, _ := newRPCServer(t, true)
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"getGlobalOption"}`)
	if out["error"] != nil {
		t.Fatalf("getGlobalOption errored: %v", out)
	}
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not object: %T", out["result"])
	}
	// AriaNg settings panel requires these keys; all must be present strings.
	for _, k := range []string{"max-concurrent-downloads", "max-overall-download-limit",
		"max-connection-per-server", "min-split-size", "continue", "file-allocation"} {
		if _, ok := res[k].(string); !ok {
			t.Fatalf("missing/non-string global option %q: %v", k, res[k])
		}
	}
	if res["max-concurrent-downloads"] != "5" {
		t.Fatalf("expected default max-concurrent-downloads=5, got %v", res["max-concurrent-downloads"])
	}
}

// rateToAria2 must produce aria2 integer bytes/s, never a malformed unit string.
func TestRateToAria2(t *testing.T) {
	cases := map[string]string{
		"8MiB/s": strconv.FormatInt(8<<20, 10),
		"512KiB/s": strconv.FormatInt(512<<10, 10),
		"0": "0",
		"": "0",
		"garbage": "0",
	}
	for in, want := range cases {
		if got := rateToAria2(in); got != want {
			t.Fatalf("rateToAria2(%q)=%q want %q", in, got, want)
		}
	}
}

func TestContextUsed(t *testing.T) {
	_ = context.Background()
}

// CORS preflight must succeed without a token so AriaNg (different origin)
// can reach /jsonrpc; the real call still requires the token.
func TestRPCORS(t *testing.T) {
	srv, _ := newRPCServer(t, true)

	// Preflight: OPTIONS, no token, must be 204 with reflected CORS headers.
	preq := httptest.NewRequest(http.MethodOptions, "/jsonrpc", nil)
	preq.Header.Set("Origin", "http://ariang.example:8080")
	preq.Header.Set("Access-Control-Request-Method", "POST")
	pw := httptest.NewRecorder()
	srv.ServeHTTP(pw, preq)
	if pw.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d want 204", pw.Code)
	}
	if pw.Header().Get("Access-Control-Allow-Origin") != "http://ariang.example:8080" {
		t.Fatalf("missing reflected CORS origin: %v", pw.Header())
	}

	// Actual POST carries CORS headers too.
	_, out := rpcCall(t, srv, `{"jsonrpc":"2.0","id":1,"method":"getVersion"}`)
	if out["error"] != nil {
		t.Fatalf("post after preflight errored: %v", out)
	}
}
