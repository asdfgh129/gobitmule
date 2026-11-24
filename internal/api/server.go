// Package api implements the L0 control plane of docs/07: REST + WebSocket.
// No aria2 JSON-RPC facade is present (ADR-0019: P0/P1 stay at L0).
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asdfgh129/gobitmule/internal/config"
	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/security"
)

//go:embed ui/index.html
var uiFS embed.FS

const (
	// wsTicketTTL bounds the window in which a browser may open the event
	// socket without exposing the long-lived bearer token in a URL.
	wsTicketTTL     = 60 * time.Second
	maxEventHistory = 200
)

type Options struct {
	Control     core.ControlPlane
	Config      *config.Manager
	Credentials core.CredentialStore
	Bus         *core.EventBus
	Logger      *slog.Logger
	AuthToken   string
	RequireAuth bool
	// EventHistory reads persisted events for the UI's initial timeline.
	// Optional: when absent, GET /events/history reports E_CAPABILITY_UNAVAILABLE.
	EventHistory func(context.Context, int) ([]core.Event, error)
}

type Server struct {
	ctl          core.ControlPlane
	cfg          *config.Manager
	creds        core.CredentialStore
	bus          *core.EventBus
	log          *slog.Logger
	hub          *wsHub
	mux          *http.ServeMux
	authToken    string
	requireAuth  bool
	mu           sync.Mutex
	idempotent   map[string]core.TaskID
	eventHistory func(context.Context, int) ([]core.Event, error)
	// rpc is the optional L1 aria2 facade (ADR-0031). Nil when disabled, in
	// which case /jsonrpc is not registered and returns 404.
	rpc *rpcFacade
	// tickets are short-lived, single-use replacements for the bearer token
	// on the WebSocket upgrade (a browser cannot set Authorization there).
	ticketMu sync.Mutex
	tickets  map[string]time.Time
}

func New(opts Options) *Server {
	if opts.Bus == nil {
		opts.Bus = core.NewEventBus(256)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{
		ctl: opts.Control, cfg: opts.Config, creds: opts.Credentials,
		bus: opts.Bus, log: opts.Logger, hub: newWSHub(opts.Bus),
		mux: http.NewServeMux(), authToken: opts.AuthToken,
		requireAuth: opts.RequireAuth, idempotent: make(map[string]core.TaskID),
		eventHistory: opts.EventHistory, tickets: make(map[string]time.Time),
	}
	// ADR-0031: opt-in L1 aria2 facade. When disabled the JSON-RPC routes are
	// not registered at all, so the wire surface stays at L0 byte-for-byte.
	if opts.Config != nil && opts.Config.Get().RPC.Enabled {
		s.rpc = newRPCFacade(s)
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	mux := s.mux
	mux.HandleFunc("GET /", s.serveUI)
	mux.HandleFunc("POST /api/v1/tasks", s.createTask)
	mux.HandleFunc("POST /api/v1/tasks/batch", s.batchTasks)
	mux.HandleFunc("GET /api/v1/tasks", s.listTasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.getTask)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", s.patchTask)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/pause", s.pauseTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/resume", s.resumeTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", s.cancelTask)
	mux.HandleFunc("POST /api/v1/resolve", s.resolveOnly)
	mux.HandleFunc("GET /api/v1/capabilities", s.capabilities)
	mux.HandleFunc("GET /api/v1/config", s.getConfig)
	mux.HandleFunc("PATCH /api/v1/config", s.patchConfig)
	mux.HandleFunc("GET /api/v1/stats", s.stats)
	mux.HandleFunc("GET /api/v1/healthz", s.health)
	mux.HandleFunc("POST /api/v1/credentials", s.putCredential)
	mux.HandleFunc("GET /api/v1/events/history", s.eventHistoryHandler)
	mux.HandleFunc("POST /api/v1/ws-ticket", s.issueWSTicket)
	mux.Handle("GET /api/v1/events", http.HandlerFunc(s.hub.ServeHTTP))
	// ADR-0031: L1 aria2 facade. Routes are always registered so a disabled
	// facade returns a clean 404 from the handler (not a 405 from the "/" fallback).
	mux.HandleFunc("POST /jsonrpc", s.jsonRPC)
	mux.HandleFunc("OPTIONS /jsonrpc", s.jsonRPCPreflight)
	mux.HandleFunc("GET /jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		if s.rpc == nil {
			http.NotFound(w, r)
			return
		}
		s.setRPCCORS(w, r)
		// AriaNg connects over WS with the token in the query string; validated
		// here with constant-time compare so the long-lived token is the only
		// secret and it is never echoed back.
		if s.requireAuth {
			if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.authToken)) != 1 {
				writeError(w, core.Err(core.E_INVALID_INPUT, "invalid or missing token"))
				return
			}
		}
		s.rpc.ws.ServeHTTP(w, r)
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("panic in api handler", "value", v)
			writeError(w, core.Err(core.E_INTERNAL, "internal server error"))
		}
	}()
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.requireAuth && strings.HasPrefix(r.URL.Path, "/api/") {
		// The browser WebSocket API cannot set an Authorization header. Remote
		// UIs first call POST /ws-ticket (bearer-authenticated), then connect to
		// /events?ticket=<single-use, 60s>. The long-lived token never enters a URL.
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/events" {
			if !s.consumeWSTicket(r.URL.Query().Get("ticket")) {
				writeError(w, core.Err(core.E_INVALID_INPUT, "invalid or expired websocket ticket"))
				return
			}
		} else {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.authToken)) != 1 {
				writeError(w, core.Err(core.E_INVALID_INPUT, "invalid or missing bearer token"))
				return
			}
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

type createRequest struct {
	Input         string             `json:"input"`
	Options       core.TaskOptions   `json:"options"`
	CredentialRef core.CredentialRef `json:"credentialRef"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	wait, err := previewWait(r)
	if err != nil {
		writeError(w, err)
		return
	}
	in, err := core.ParseInput(req.Input)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.CredentialRef != "" {
		in.CredentialRef = req.CredentialRef
		req.Options.CredentialRef = req.CredentialRef
	}
	if err := validateTaskOptions(req.Options); err != nil {
		writeError(w, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		s.mu.Lock()
		id, ok := s.idempotent[key]
		s.mu.Unlock()
		if ok {
			body := s.createdTaskBody(r.Context(), id, 0, map[string]any{"idempotent": true})
			writeJSON(w, http.StatusOK, body)
			return
		}
	}
	id, err := s.ctl.Create(r.Context(), in, req.Options)
	if err != nil {
		writeError(w, err)
		return
	}
	if key != "" {
		s.mu.Lock()
		s.idempotent[key] = id
		s.mu.Unlock()
	}
	writeJSON(w, http.StatusCreated, s.createdTaskBody(r.Context(), id, wait, nil))
}

const (
	// previewWaitMax bounds how long POST /tasks may hold a request while it
	// waits for the scheduler to publish the Resource (ADR-0020 follow-up:
	// "POST /tasks 响应内嵌 Resource 预览"). Creation has already happened by
	// then, so the wait is a UX optimisation, never a precondition.
	previewWaitMax = 5 * time.Second
	// previewPollInterval is the scheduler-visibility granularity of that wait.
	previewPollInterval = 20 * time.Millisecond
)

// previewWait parses the optional `?wait=<ms>` opt-in for the Resource preview.
// Absent means "never block"; the default response stays as fast as before.
func previewWait(r *http.Request) (time.Duration, error) {
	raw := r.URL.Query().Get("wait")
	if raw == "" {
		return 0, nil
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 || time.Duration(ms)*time.Millisecond > previewWaitMax {
		return 0, core.Err(core.E_INVALID_INPUT,
			"wait must be 1..%d (milliseconds)", previewWaitMax/time.Millisecond)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// createdTaskBody builds the POST /api/v1/tasks response.
//
// ADR-0020 lists "POST /tasks 响应内嵌 Resource 预览" as a P1 follow-up. We
// deliver it as a *best-effort* preview rather than by resolving synchronously
// inside the request: Create enqueues the task and resolution happens in the
// worker, so a response that always carried a Resource would have to block every
// caller (including `udctl`) on a network round trip.
//
//	(no query)      → immediate response; `resource` is filled only if the
//
//	                  scheduler already published it, `resolved` says which.
//	?wait=<ms>      → poll up to that bound for the resolution; `previewTimedOut`
//
//	                  marks the case where creation succeeded but the preview
//	                  did not arrive in time.
//
// The original `id` / `state` / `createdAt` keys are kept for compatibility; the
// inlined `task` is authoritative for state (a task may already have left
// Pending by the time we read it back).
func (s *Server) createdTaskBody(ctx context.Context, id core.TaskID,
	wait time.Duration, extra map[string]any) map[string]any {
	body := map[string]any{
		"id": id, "state": string(core.StatePending),
		"createdAt": core.TimeFromID(id),
		"resource":  nil, "resolved": false,
	}
	for k, v := range extra {
		body[k] = v
	}
	d, err := s.ctl.Get(ctx, id)
	if err != nil || d == nil {
		// Creation succeeded; failing to echo it back must not fail the request.
		return body
	}
	if wait > 0 && d.Resource == nil && previewPending(d.State) {
		if resolved, ok := s.awaitResource(ctx, id, wait); ok {
			d = resolved
		} else {
			body["previewTimedOut"] = true
		}
	}
	body["task"] = d
	body["state"] = string(d.State)
	if d.Resource != nil {
		body["resource"] = d.Resource
		body["resolved"] = true
	}
	return body
}

// previewPending reports whether the scheduler may still publish a Resource.
func previewPending(state core.TaskState) bool {
	return state == core.StatePending || state == core.StateResolving
}

// awaitResource polls until the Resource appears, the task stops being
// resolvable, or the deadline passes.
func (s *Server) awaitResource(ctx context.Context, id core.TaskID,
	wait time.Duration) (*core.TaskDetail, bool) {
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, false
		}
		d, err := s.ctl.Get(ctx, id)
		if err != nil || d == nil {
			return nil, false
		}
		if d.Resource != nil || !previewPending(d.State) {
			return d, d.Resource != nil
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(previewPollInterval):
		}
	}
}

func (s *Server) batchTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Inputs []string         `json:"inputs"`
		Action string           `json:"action"`
		IDs    []core.TaskID    `json:"ids"`
		Option core.TaskOptions `json:"options"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := validateTaskOptions(req.Option); err != nil {
		writeError(w, err)
		return
	}
	ids := make([]core.TaskID, 0, len(req.Inputs)+len(req.IDs))
	for _, raw := range req.Inputs {
		in, err := core.ParseInput(raw)
		if err != nil {
			writeError(w, err)
			return
		}
		id, err := s.ctl.Create(r.Context(), in, req.Option)
		if err != nil {
			writeError(w, err)
			return
		}
		ids = append(ids, id)
	}
	for _, id := range req.IDs {
		switch req.Action {
		case "pause":
			_ = s.ctl.Pause(r.Context(), id)
		case "resume":
			_ = s.ctl.Resume(r.Context(), id)
		case "cancel":
			_ = s.ctl.Cancel(r.Context(), id)
		}
		ids = append(ids, id)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ids": ids, "count": len(ids)})
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	f := core.TaskFilter{
		State: core.TaskState(q.Get("state")), Kind: core.InputKind(q.Get("kind")),
		Cursor: q.Get("cursor"), Limit: limit,
	}
	tasks, total, err := s.ctl.List(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	next := ""
	if len(tasks) > 0 {
		next = string(tasks[len(tasks)-1].ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "total": total, "nextCursor": next})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	d, err := s.ctl.Get(r.Context(), core.TaskID(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	var opt core.TaskOptions
	if err := decode(r, &opt); err != nil {
		writeError(w, err)
		return
	}
	if err := validateTaskOptions(opt); err != nil {
		writeError(w, err)
		return
	}
	id := core.TaskID(r.PathValue("id"))
	if err := s.ctl.SetOption(r.Context(), id, opt); err != nil {
		writeError(w, err)
		return
	}
	d, err := s.ctl.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	err := s.ctl.Delete(r.Context(), core.TaskID(r.PathValue("id")),
		r.URL.Query().Get("withFiles") == "true")
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pauseTask(w http.ResponseWriter, r *http.Request) {
	s.simpleAction(w, r, s.ctl.Pause)
}
func (s *Server) resumeTask(w http.ResponseWriter, r *http.Request) {
	s.simpleAction(w, r, s.ctl.Resume)
}
func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	s.simpleAction(w, r, s.ctl.Cancel)
}

func (s *Server) simpleAction(w http.ResponseWriter, r *http.Request,
	fn func(ctx context.Context, id core.TaskID) error) {
	id := core.TaskID(r.PathValue("id"))
	if err := fn(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "accepted": true})
}

func (s *Server) resolveOnly(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	in, err := core.ParseInput(req.Input)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := s.ctl.ResolveOnly(r.Context(), in, req.Options)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.ctl.Capabilities())
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Get())
}

func (s *Server) patchConfig(w http.ResponseWriter, r *http.Request) {
	var kv map[string]any
	if err := decode(r, &kv); err != nil {
		writeError(w, err)
		return
	}
	next, err := s.cfg.Patch(kv)
	if err != nil {
		writeError(w, core.Err(core.E_INVALID_INPUT, "%v", err))
		return
	}
	s.bus.Publish(core.Event{Type: core.EventConfigChanged, Payload: map[string]any{"keys": len(kv)}})
	writeJSON(w, http.StatusOK, next)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	v, err := s.ctl.Stats(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	h := s.ctl.Health(r.Context())
	code := http.StatusOK
	if h.Status != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, h)
}

func (s *Server) putCredential(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value string `json:"value"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if s.creds == nil {
		writeError(w, core.Err(core.E_CAPABILITY_UNAVAILABLE, "credential store is unavailable"))
		return
	}
	ref, err := s.creds.Put(r.Context(), req.Value)
	if err != nil {
		writeError(w, err)
		return
	}
	// The response contains only the reference, never the secret.
	writeJSON(w, http.StatusCreated, map[string]any{"credentialRef": ref})
}

// eventHistoryHandler backs the UI's initial event timeline. Live updates still
// arrive over /events; this only fills the gap before the socket connects and
// survives a page reload. Persisted events are already written by the scheduler.
func (s *Server) eventHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if s.eventHistory == nil {
		writeError(w, core.Err(core.E_CAPABILITY_UNAVAILABLE, "event history is not available"))
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, core.Err(core.E_INVALID_INPUT, "invalid limit"))
			return
		}
		limit = n
	}
	if limit > maxEventHistory {
		limit = maxEventHistory
	}
	events, err := s.eventHistory(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if events == nil {
		events = []core.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// issueWSTicket mints a single-use, short-lived credential for the WebSocket
// upgrade. It is itself protected by the normal bearer check in ServeHTTP.
func (s *Server) issueWSTicket(w http.ResponseWriter, _ *http.Request) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		writeError(w, core.Err(core.E_INTERNAL, "cannot create websocket ticket"))
		return
	}
	ticket := hex.EncodeToString(b)
	expires := time.Now().Add(wsTicketTTL)
	now := time.Now()
	s.ticketMu.Lock()
	for k, exp := range s.tickets {
		if now.After(exp) {
			delete(s.tickets, k)
		}
	}
	s.tickets[ticket] = expires
	s.ticketMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"ticket": ticket, "expiresAt": expires.UnixMilli(),
	})
}

func (s *Server) consumeWSTicket(ticket string) bool {
	if ticket == "" {
		return false
	}
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	expires, ok := s.tickets[ticket]
	if !ok {
		return false
	}
	delete(s.tickets, ticket)
	return time.Now().Before(expires)
}

// ---------- helpers ----------

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return core.Err(core.E_INVALID_INPUT, "invalid JSON body: %v", err)
	}
	return nil
}

// validateTaskOptions rejects inline credential headers. Secrets must be
// registered via POST /credentials and referenced by credentialRef, so the DB
// never holds a plaintext Cookie/Authorization (docs/06 §6, docs/07 §2).
func validateTaskOptions(opt core.TaskOptions) error {
	for k := range opt.Headers {
		if security.IsSensitiveHeader(k) {
			return core.Err(core.E_INVALID_INPUT,
				"header %q must not be sent inline; use POST /credentials and credentialRef", k)
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	e := core.AsError(err)
	// Never echo raw URLs or secrets in error details.
	e.Message = security.Redact(e.Message)
	for k, v := range e.Details {
		if str, ok := v.(string); ok {
			e.Details[k] = security.Redact(str)
		}
	}
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": e})
}

func marshalEvent(e core.Event) ([]byte, error) { return json.Marshal(e) }
