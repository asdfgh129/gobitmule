package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/asdfgh129/gobitmule/internal/config"
	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/security"
)

// authRequest is a local helper because the shared request() helper is for the
// loopback (no-auth) server used by the REST contract tests.
func authRequest(t *testing.T, method, url, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func newAuthTestServer(t *testing.T, history func(context.Context, int) ([]core.Event, error)) *httptest.Server {
	t.Helper()
	srv := New(Options{
		Control: newFakeControl(), Config: config.NewManager(config.Defaults(), nil),
		Credentials: security.NewMemoryCredentialStore(), Bus: core.NewEventBus(16),
		AuthToken: "ui-test-token", RequireAuth: true, EventHistory: history,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func TestEventHistoryEndpoint(t *testing.T) {
	events := []core.Event{
		{Type: core.EventTaskState, TaskID: core.TaskID("t1"), Ts: 1762570000000,
			Payload: map[string]any{"state": "Running"}},
		{Type: core.EventConfigChanged, Ts: 1762570001000, Payload: map[string]any{"keys": 1}},
	}
	var gotLimit int
	ts := newAuthTestServer(t, func(_ context.Context, limit int) ([]core.Event, error) {
		gotLimit = limit
		return events, nil
	})

	code, body := authRequest(t, "GET", ts.URL+"/api/v1/events/history?limit=3", "ui-test-token", "")
	if code != 200 || len(body["events"].([]any)) != 2 {
		t.Fatalf("history: %d %v", code, body)
	}
	if gotLimit != 3 {
		t.Fatalf("limit = %d, want 3", gotLimit)
	}

	code, _ = authRequest(t, "GET", ts.URL+"/api/v1/events/history?limit=9999", "ui-test-token", "")
	if code != 200 || gotLimit != maxEventHistory {
		t.Fatalf("limit clamp: %d (got %d)", code, gotLimit)
	}
	code, body = authRequest(t, "GET", ts.URL+"/api/v1/events/history?limit=0", "ui-test-token", "")
	if code != 400 || errCode(body) != core.E_INVALID_INPUT {
		t.Fatalf("invalid limit: %d %v", code, body)
	}
	code, _ = authRequest(t, "GET", ts.URL+"/api/v1/events/history", "", "")
	if code != 400 {
		t.Fatalf("history must require bearer: %d", code)
	}
}

func TestEventHistoryUnavailable(t *testing.T) {
	ts := newAuthTestServer(t, nil)
	code, body := authRequest(t, "GET", ts.URL+"/api/v1/events/history", "ui-test-token", "")
	if code != 503 || errCode(body) != core.E_CAPABILITY_UNAVAILABLE {
		t.Fatalf("unavailable: %d %v", code, body)
	}
}

func TestEventHistoryEmptyIsArray(t *testing.T) {
	ts := newAuthTestServer(t, func(context.Context, int) ([]core.Event, error) {
		return nil, nil
	})
	code, body := authRequest(t, "GET", ts.URL+"/api/v1/events/history", "ui-test-token", "")
	if code != 200 {
		t.Fatalf("empty history: %d %v", code, body)
	}
	events, ok := body["events"].([]any)
	if !ok || len(events) != 0 {
		t.Fatalf("events must be an empty array, got %#v", body["events"])
	}
}

func TestWebSocketTicket(t *testing.T) {
	ts := newAuthTestServer(t, nil)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/events"

	// A raw browser cannot set Authorization, so the upgrade is rejected.
	if _, _, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil {
		t.Fatal("websocket upgrade without ticket must be rejected")
	}

	code, body := authRequest(t, "POST", ts.URL+"/api/v1/ws-ticket", "ui-test-token", "")
	if code != 201 || body["ticket"] == nil || body["expiresAt"] == nil {
		t.Fatalf("ticket: %d %v", code, body)
	}
	ticket := body["ticket"].(string)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?ticket="+ticket, nil)
	if err != nil {
		t.Fatalf("upgrade with ticket: %v", err)
	}
	_ = conn.Close()

	// Tickets are single-use: replaying the same URL must fail.
	if _, _, err := websocket.DefaultDialer.Dial(wsURL+"?ticket="+ticket, nil); err == nil {
		t.Fatal("ticket replay must be rejected")
	}
	code, body = authRequest(t, "POST", ts.URL+"/api/v1/ws-ticket", "", "")
	if code != 400 || errCode(body) != core.E_INVALID_INPUT {
		t.Fatalf("ticket issue must require bearer: %d %v", code, body)
	}
}
