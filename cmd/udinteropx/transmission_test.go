package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A checking torrent is at 100% but must never be reported as seeding: BT-03's
// download half has to start from a peer that actually offers the bytes.
func TestTRSeedingRequiresActiveStatus(t *testing.T) {
	cases := []struct {
		name string
		in   trTorrent
		want bool
	}{
		{"seeding at 100%", trTorrent{PercentDone: 1, Status: trStatusSeeding}, true},
		{"seed-wait at 100%", trTorrent{PercentDone: 1, Status: trStatusSeedWait}, true},
		{"still checking at 100%", trTorrent{PercentDone: 1, Status: trStatusChecking}, false},
		{"downloading at 99%", trTorrent{PercentDone: 0.99, Status: trStatusDownloading}, false},
		{"stopped at 100%", trTorrent{PercentDone: 1, Status: trStatusStopped}, false},
	}
	for _, c := range cases {
		if got := trSeeding(c.in); got != c.want {
			t.Errorf("%s: trSeeding = %t, want %t", c.name, got, c.want)
		}
	}
}

// Completion needs both a finished progress ratio and the full byte count, so
// a truncated torrent can never satisfy the leecher half-direction.
func TestTRCompleteRequiresAllBytes(t *testing.T) {
	cases := []struct {
		name string
		in   trTorrent
		want bool
	}{
		{"complete", trTorrent{PercentDone: 1, TotalSize: 100, DownloadedEver: 100}, true},
		{"over-received", trTorrent{PercentDone: 1, TotalSize: 100, DownloadedEver: 120}, true},
		{"short bytes", trTorrent{PercentDone: 1, TotalSize: 100, DownloadedEver: 99}, false},
		{"no size", trTorrent{PercentDone: 1, TotalSize: 0, DownloadedEver: 0}, false},
		{"partial percent", trTorrent{PercentDone: 0.5, TotalSize: 100, DownloadedEver: 50}, false},
	}
	for _, c := range cases {
		if got := trComplete(c.in); got != c.want {
			t.Errorf("%s: trComplete = %t, want %t", c.name, got, c.want)
		}
	}
}

func TestTRStatusNameCoversChecking(t *testing.T) {
	if got := trStatusName(trStatusChecking); got != "checking" {
		t.Fatalf("trStatusName(checking) = %q", got)
	}
	if got := trStatusName(99); !strings.HasPrefix(got, "unknown") {
		t.Fatalf("trStatusName(99) = %q, want an explicit unknown marker", got)
	}
}

// Transmission answers 409 with a session id until the client echoes it. A
// client that treats 409 as fatal can never talk to a real daemon.
func TestTRRPCNegotiatesSessionID(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("X-Transmission-Session-Id") != "sid-1" {
			w.Header().Set("X-Transmission-Session-Id", "sid-1")
			w.WriteHeader(http.StatusConflict)
			return
		}
		body, _ := json.Marshal(map[string]interface{}{
			"result": "success",
			"arguments": map[string]interface{}{
				"current-stats": map[string]interface{}{"uploadedBytes": 4096},
			},
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := &trRPC{base: srv.URL, client: srv.Client()}
	up, err := c.sessionUploaded()
	if err != nil {
		t.Fatalf("sessionUploaded: %v", err)
	}
	if up != 4096 {
		t.Fatalf("uploadedBytes = %d, want 4096", up)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("handler hits = %d, want 2 (409 then the retried call)", got)
	}
	if c.session != "sid-1" {
		t.Fatalf("session id = %q, want sid-1", c.session)
	}
}

// A non-success RPC result is a hard error: silently ignoring it would let a
// failed torrent-add be reported as a completed matrix.
func TestTRRPCRejectsNonSuccessResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":"torrent not found","arguments":{}}`))
	}))
	defer srv.Close()

	c := &trRPC{base: srv.URL, client: srv.Client()}
	if err := c.verify(1); err == nil {
		t.Fatal("want an error for a non-success RPC result")
	}
}

// An unsupported client name must produce an explicit UNSUPPORTED record, not
// an empty report that could be read as "nothing to check".
func TestRunPeerMatrixUnsupportedClientIsExplicit(t *testing.T) {
	rep := runPeerMatrix("aria2")
	if rep.AllPass {
		t.Fatal("an unexecuted matrix must never report all_pass")
	}
	if len(rep.Cases) != 1 || rep.Cases[0].Status != "UNSUPPORTED" {
		t.Fatalf("cases = %+v, want one UNSUPPORTED record", rep.Cases)
	}
	if rep.NonClaim == "" {
		t.Fatal("report must carry the non-claim text")
	}
}
