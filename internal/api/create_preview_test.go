package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// Default POST /tasks must never block: the preview is inlined only when the
// scheduler has already published the Resource.
func TestCreateTaskInlinesKnownResource(t *testing.T) {
	ts, _, ctl := newTestServer(t)
	ctl.mu.Lock()
	ctl.resolveOn = 1 // the very first Get (inside the response builder) sees it
	ctl.mu.Unlock()

	code, body := request(t, "POST", ts.URL+"/api/v1/tasks", `{"input":"https://example.com/a.iso"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	if body["resolved"] != true {
		t.Fatalf("resolved = %v, want true", body["resolved"])
	}
	if body["resource"] == nil {
		t.Fatal("resource preview missing")
	}
	if body["task"] == nil {
		t.Fatal("inlined task missing")
	}
	// The inlined task is authoritative: state must follow it, not the
	// creation-time "Pending".
	if body["state"] != string(core.StateReady) {
		t.Fatalf("state = %v, want %s", body["state"], core.StateReady)
	}
	if body["id"] == "" || body["createdAt"] == nil {
		t.Fatalf("compatibility keys missing: %v", body)
	}
}

// Without the opt-in wait the handler reports the truth: not resolved yet, and
// it must not pretend otherwise or flag a timeout it never waited for.
func TestCreateTaskPreviewNotResolvedDoesNotWait(t *testing.T) {
	ts, _, _ := newTestServer(t)
	start := time.Now()
	code, body := request(t, "POST", ts.URL+"/api/v1/tasks", `{"input":"https://example.com/a.iso"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("unexpected wait without ?wait: %v", elapsed)
	}
	if body["resolved"] != false || body["resource"] != nil {
		t.Fatalf("resolved/resource = %v/%v, want false/nil", body["resolved"], body["resource"])
	}
	if _, ok := body["previewTimedOut"]; ok {
		t.Fatal("previewTimedOut must only appear when a wait actually expired")
	}
	if body["task"] == nil {
		t.Fatal("inlined task missing")
	}
}

func TestCreateTaskWaitReturnsResource(t *testing.T) {
	ts, _, ctl := newTestServer(t)
	ctl.mu.Lock()
	ctl.resolveOn = 3 // first Get no resource, then the poll loop picks it up
	ctl.mu.Unlock()

	code, body := request(t, "POST", ts.URL+"/api/v1/tasks?wait=2000",
		`{"input":"https://example.com/a.iso"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	if body["resolved"] != true || body["resource"] == nil {
		t.Fatalf("wait did not surface the resource: %v", body)
	}
	if _, ok := body["previewTimedOut"]; ok {
		t.Fatalf("previewTimedOut set although the resource arrived: %v", body)
	}
}

func TestCreateTaskWaitTimesOutHonestly(t *testing.T) {
	ts, _, _ := newTestServer(t)
	start := time.Now()
	code, body := request(t, "POST", ts.URL+"/api/v1/tasks?wait=60",
		`{"input":"https://example.com/a.iso"}`)
	if code != http.StatusCreated {
		t.Fatalf("creation must succeed even when the preview times out: %d %v", code, body)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("returned after %v, expected to wait for the bound", elapsed)
	}
	if body["resolved"] != false || body["resource"] != nil {
		t.Fatalf("resolved/resource = %v/%v, want false/nil", body["resolved"], body["resource"])
	}
	if body["previewTimedOut"] != true {
		t.Fatalf("previewTimedOut = %v, want true", body["previewTimedOut"])
	}
}

func TestCreateTaskRejectsBadWait(t *testing.T) {
	ts, _, ctl := newTestServer(t)
	for _, q := range []string{"wait=0", "wait=abc", "wait=999999"} {
		code, body := request(t, "POST", ts.URL+"/api/v1/tasks?"+q,
			`{"input":"https://example.com/a.iso"}`)
		if code != http.StatusBadRequest || errCode(body) != core.E_INVALID_INPUT {
			t.Fatalf("%s: %d %v, want 400 %s", q, code, body, core.E_INVALID_INPUT)
		}
	}
	if n := ctl.calls["create"]; n != 0 {
		t.Fatalf("create was called %d times despite an invalid wait", n)
	}
}
