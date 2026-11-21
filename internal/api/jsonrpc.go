// Package api — L1 aria2 JSON-RPC facade (ADR-0031, docs/07 §5).
//
// This file implements the optional, opt-in aria2 compatibility subset. It is
// a pure mapping layer over core.ControlPlane: no aria2c binary (ADR-0003),
// no third-party download engine (ADR-0002), no new wire protocol on the core.
// When config.RPC.Enabled is false the handlers are not registered at all, so
// /jsonrpc returns 404 and capabilities.rpcLevel stays "L0" byte-for-byte.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/asdfgh129/ud/internal/config"
	"github.com/asdfgh129/ud/internal/core"
)

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 wire types (docs/07 §5.1)
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	rpcErrParse     = -32700
	rpcErrInvalid   = -32600
	rpcErrMethod    = -32601
	rpcErrParams    = -32602
	rpcErrInternal  = -32603
	rpcErrAppBase   = -32000 // aria2 reserves -32000..-32099 for app errors
	rpcTokenPrefix  = "token:"
	rpcGIDBytes     = 8 // 16 hex chars
)

// gidMap is the bidirectional GID(16 hex) <-> TaskID(ULID) registry (docs/07 §5.1).
// GIDs are process-scoped; a restart rebuilds them lazily from List(). The map
// is advisory: GID lookups that miss are resolved by a full List scan so a
// task created before a restart remains addressable.
type gidMap struct {
	mu      sync.RWMutex
	byGID   map[string]core.TaskID
	byTask  map[core.TaskID]string
}

func newGIDMap() *gidMap {
	return &gidMap{byGID: map[string]core.TaskID{}, byTask: map[core.TaskID]string{}}
}

// newGID derives a deterministic-in-process GID from the ULID via SHA-256.
// Using a hash (rather than random) keeps the mapping stable across restarts
// for the same task id, so stale AriaNg GIDs keep working after a daemon restart.
func newGID(id core.TaskID) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:rpcGIDBytes])
}

func (m *gidMap) put(id core.TaskID) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.byTask[id]; ok {
		return g
	}
	g := newGID(id)
	m.byTask[id] = g
	m.byGID[g] = id
	return g
}

func (m *gidMap) task(gid string) (core.TaskID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.byGID[gid]
	return id, ok
}

// rpcFacade holds the facade state. It is created once in New() when RPC is on.
type rpcFacade struct {
	s   *Server
	gid *gidMap
	ws  *aria2WSHub
	// verbosity/dry-run are accepted-but-ignored per ADR-0019 (unknown options
	// must not break front-ends); kept as fields for future multicall state.
	mu sync.Mutex
}

func newRPCFacade(s *Server) *rpcFacade {
	f := &rpcFacade{s: s, gid: newGIDMap(), ws: newAria2WSHub(s.bus)}
	f.ws.start()
	return f
}

// ---------------------------------------------------------------------------
// HTTP + WebSocket handlers
// ---------------------------------------------------------------------------

// setRPCCORS mirrors aria2's --rpc-allow-origin-all for the opt-in facade:
// AriaNg is served from a different origin and the browser preflights POST.
// Reflected origin is safe here because every state-changing call still
// requires the bearer/token secret (constant-time compared); a cross-site
// page cannot obtain it. Loopback/no-auth deployments have no data to steal
// beyond what is already locally reachable.
func (s *Server) setRPCCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Max-Age", "600")
}

// jsonRPCPreflight answers the browser CORS preflight without requiring a
// token (preflights never carry credentials). The real request is authenticated.
func (s *Server) jsonRPCPreflight(w http.ResponseWriter, r *http.Request) {
	if s.rpc == nil {
		http.NotFound(w, r)
		return
	}
	s.setRPCCORS(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) jsonRPC(w http.ResponseWriter, r *http.Request) {
	if s.rpc == nil {
		http.NotFound(w, r)
		return
	}
	s.setRPCCORS(w, r)
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		s.rpc.writeErr(w, nil, rpcErrParse, "parse error")
		return
	}
	trimmed := strings.TrimSpace(string(raw))
	var out any
	if strings.HasPrefix(trimmed, "[") {
		var batch []json.RawMessage
		if err := json.Unmarshal(raw, &batch); err != nil {
			s.rpc.writeErr(w, nil, rpcErrParse, "parse error")
			return
		}
		results := make([]any, 0, len(batch))
		for _, item := range batch {
			if res := s.rpc.dispatchOne(r.Context(), item, r.Header.Get("Authorization")); res != nil {
				results = append(results, res)
			}
		}
		if len(results) == 0 {
			s.rpc.writeErr(w, nil, rpcErrInvalid, "empty batch")
			return
		}
		out = results
	} else {
		out = s.rpc.dispatchOne(r.Context(), raw, r.Header.Get("Authorization"))
		if out == nil {
			s.rpc.writeErr(w, nil, rpcErrInvalid, "invalid request")
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *rpcFacade) dispatchOne(ctx context.Context, raw json.RawMessage, authHeader string) any {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return f.errResp(nil, rpcErrParse, "parse error")
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return f.errResp(req.ID, rpcErrInvalid, "invalid request")
	}
	// aria2 convention: token may be the first params element ("token:secret")
	// or a prefix on the jsonrpc field ("token:secret:2.0"). Bearer header is
	// accepted too (handled by ServeHTTP for /api/; here we check both).
	params, authToken := extractRPCParams(req.Params)
	if authToken == "" {
		authToken = strings.TrimPrefix(authHeader, "Bearer ")
	}
	if !f.s.rpcAuthorized(authToken) {
		return f.errResp(req.ID, rpcErrAppBase-1, "Unauthorized")
	}

	result, rerr := f.call(ctx, req.Method, params)
	if rerr != nil {
		return f.errResp(req.ID, rerr.Code, rerr.Message)
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func (f *rpcFacade) errResp(id json.RawMessage, code int, msg string) *rpcResponse {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func (f *rpcFacade) writeErr(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeJSON(w, http.StatusOK, f.errResp(id, code, msg))
}

// extractRPCParams returns the positional params with an optional leading
// "token:<secret>" element removed, plus that token.
func extractRPCParams(raw json.RawMessage) ([]json.RawMessage, string) {
	if len(raw) == 0 {
		return nil, ""
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return []json.RawMessage{raw}, ""
	}
	if len(arr) > 0 {
		var first string
		if json.Unmarshal(arr[0], &first) == nil && strings.HasPrefix(first, rpcTokenPrefix) {
			return arr[1:], strings.TrimPrefix(first, rpcTokenPrefix)
		}
	}
	return arr, ""
}

// rpcAuthorized implements fail-closed auth for /jsonrpc. It accepts the aria2
// token: secret or a Bearer token, compared in constant time. When auth is not
// required (loopback without token) it returns true.
func (s *Server) rpcAuthorized(token string) bool {
	if !s.requireAuth {
		return true
	}
	got := token
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.authToken)) == 1 {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Method dispatch — compat subset only (docs/07 §5.2). Unknown methods are
// rejected with -32601; unknown OPTIONS are ignored inside changeOption.
// ---------------------------------------------------------------------------

var rpcMethods = map[string]bool{
	"addUri": true, "addTorrent": true, "addMetalink": true,
	"pause": true, "pauseAll": true, "forcePause": true, "forcePauseAll": true,
	"unpause": true, "unpauseAll": true,
	"remove": true, "forceRemove": true, "removeDownloadResult": true,
	"tellStatus": true, "tellActive": true, "tellWaiting": true, "tellStopped": true,
	"getUris": true, "getFiles": true, "getPeers": true, "getServers": true,
	"getGlobalStat": true, "getOption": true, "getGlobalOption": true,
	"changeOption": true, "changeGlobalOption": true, "changeUri": true, "changePosition": true,
	"getVersion": true, "getSessionInfo": true, "saveSession": true,
	"shutdown": true, "forceShutdown": true,
	"system.multicall": true, "system.listMethods": true, "system.listNotifications": true,
}

var rpcNotifications = []string{
	"aria2.onDownloadStart", "aria2.onDownloadPause", "aria2.onDownloadStop",
	"aria2.onDownloadComplete", "aria2.onDownloadError", "aria2.onBtDownloadComplete",
}

func (f *rpcFacade) call(ctx context.Context, method string, params []json.RawMessage) (any, *rpcError) {
	if !rpcMethods[method] {
		return nil, &rpcError{rpcErrMethod, "Method not found: " + method}
	}
	switch method {
	case "addUri":
		return f.addURI(ctx, params)
	case "addTorrent", "addMetalink":
		return nil, &rpcError{rpcErrAppBase, method + " is not supported by this build"}
	case "pause", "forcePause":
		return f.control(ctx, params, f.s.ctl.Pause)
	case "unpause":
		return f.control(ctx, params, f.s.ctl.Resume)
	case "pauseAll", "unpauseAll", "forcePauseAll":
		return f.controlAll(ctx, method)
	case "remove", "forceRemove", "removeDownloadResult":
		return f.remove(ctx, params, method == "removeDownloadResult")
	case "tellStatus":
		return f.tellStatus(ctx, params)
	case "tellActive", "tellWaiting", "tellStopped":
		return f.tellList(ctx, method)
	case "getUris":
		return f.getUris(ctx, params)
	case "getFiles":
		return f.getFiles(ctx, params)
	case "getPeers":
		return f.getPeers(ctx, params)
	case "getServers":
		return f.getServers(ctx, params)
	case "getGlobalStat":
		return f.getGlobalStat(ctx)
	case "getOption":
		return f.getOption(ctx, params)
	case "getGlobalOption":
		return f.getGlobalOption()
	case "changeOption", "changeGlobalOption":
		return f.changeOption(ctx, params, method)
	case "changeUri", "changePosition":
		return nil, &rpcError{rpcErrAppBase, method + " is not supported by this build"}
	case "getVersion":
		return map[string]any{"version": "1.37.0-ud", "features": []string{}}, nil
	case "getSessionInfo":
		return map[string]any{"sessionId": hex.EncodeToString(randomBytes(8))}, nil
	case "saveSession":
		return "OK", nil // SQLite is the single source of truth (ADR-0019).
	case "shutdown", "forceShutdown":
		return "", nil // no-op: daemon lifecycle is out of the facade's scope
	case "system.multicall":
		return f.multicall(ctx, params)
	case "system.listMethods":
		keys := make([]string, 0, len(rpcMethods))
		for k := range rpcMethods {
			keys = append(keys, k)
		}
		return keys, nil
	case "system.listNotifications":
		return rpcNotifications, nil
	}
	return nil, &rpcError{rpcErrMethod, "Method not found: " + method}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// aria2Notification maps a core event to the aria2 WS notification method.
// Only the six documented notifications are emitted; unrelated events yield
// an empty method and are skipped by the caller.
func aria2Notification(e core.Event) (method string, gid string) {
	switch e.Type {
	case core.EventTaskCreated:
		return "aria2.onDownloadStart", newGID(e.TaskID)
	case core.EventTaskState:
		if s, ok := e.Payload["state"].(core.TaskState); ok {
			switch s {
			case core.StatePaused:
				return "aria2.onDownloadPause", newGID(e.TaskID)
			case core.StateCompleted, core.StateSeeding:
				return "aria2.onDownloadComplete", newGID(e.TaskID)
			case core.StateFailed:
				return "aria2.onDownloadError", newGID(e.TaskID)
			case core.StateCanceled:
				return "aria2.onDownloadStop", newGID(e.TaskID)
			}
		}
	}
	return "", ""
}

// ---------------------------------------------------------------------------
// Individual methods (thin adapters over ControlPlane)
// ---------------------------------------------------------------------------

func (f *rpcFacade) addURI(ctx context.Context, params []json.RawMessage) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "addUri requires a URI array"}
	}
	var uris []string
	if json.Unmarshal(params[0], &uris) != nil || len(uris) == 0 {
		return nil, &rpcError{rpcErrParams, "addUri requires a non-empty URI array"}
	}
	opt := core.TaskOptions{}
	if len(params) > 1 {
		applyRPCOptions(&opt, params[1])
	}
	in, err := core.ParseInput(uris[0])
	if err != nil {
		return nil, &rpcError{rpcErrParams, err.Error()}
	}
	id, err := f.s.ctl.Create(ctx, in, opt)
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	return f.gid.put(id), nil
}

func (f *rpcFacade) control(ctx context.Context, params []json.RawMessage,
	fn func(context.Context, core.TaskID) error) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "missing GID"}
	}
	var gid string
	if json.Unmarshal(params[0], &gid) != nil {
		return nil, &rpcError{rpcErrParams, "invalid GID"}
	}
	id, ok := f.resolveGID(ctx, gid)
	if !ok {
		return nil, &rpcError{rpcErrAppBase, "GID not found"}
	}
	if err := fn(ctx, id); err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	return gid, nil
}

func (f *rpcFacade) controlAll(ctx context.Context, method string) (any, *rpcError) {
	tasks, _, err := f.s.ctl.List(ctx, core.TaskFilter{})
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	for _, t := range tasks {
		if method == "pauseAll" || method == "forcePauseAll" {
			_ = f.s.ctl.Pause(ctx, t.ID)
		} else {
			_ = f.s.ctl.Resume(ctx, t.ID)
		}
	}
	return "OK", nil
}

func (f *rpcFacade) remove(ctx context.Context, params []json.RawMessage, withFiles bool) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "missing GID"}
	}
	var gid string
	if json.Unmarshal(params[0], &gid) != nil {
		return nil, &rpcError{rpcErrParams, "invalid GID"}
	}
	id, ok := f.resolveGID(ctx, gid)
	if !ok {
		return nil, &rpcError{rpcErrAppBase, "GID not found"}
	}
	_ = f.s.ctl.Cancel(ctx, id)
	if err := f.s.ctl.Delete(ctx, id, withFiles); err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	return "OK", nil
}

func (f *rpcFacade) resolveGID(ctx context.Context, gid string) (core.TaskID, bool) {
	if id, ok := f.gid.task(gid); ok {
		return id, true
	}
	// Rebuild from List so pre-restart GIDs remain addressable.
	tasks, _, err := f.s.ctl.List(ctx, core.TaskFilter{})
	if err != nil {
		return "", false
	}
	for _, t := range tasks {
		if newGID(t.ID) == gid {
			f.gid.put(t.ID)
			return t.ID, true
		}
	}
	return "", false
}

func (f *rpcFacade) tellStatus(ctx context.Context, params []json.RawMessage) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "missing GID"}
	}
	var gid string
	if json.Unmarshal(params[0], &gid) != nil {
		return nil, &rpcError{rpcErrParams, "invalid GID"}
	}
	id, ok := f.resolveGID(ctx, gid)
	if !ok {
		return nil, &rpcError{rpcErrAppBase, "GID not found"}
	}
	d, err := f.s.ctl.Get(ctx, id)
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	return gidStatus(gid, d), nil
}

func (f *rpcFacade) tellList(ctx context.Context, method string) (any, *rpcError) {
	state := core.TaskState("")
	switch method {
	case "tellActive":
		// active == all non-terminal, non-paused tasks
	case "tellWaiting":
		state = core.StateReady
	case "tellStopped":
		tasks, _, err := f.s.ctl.List(ctx, core.TaskFilter{})
		if err != nil {
			return nil, &rpcError{rpcErrAppBase, err.Error()}
		}
		out := make([]any, 0)
		for _, t := range tasks {
			if core.IsTerminal(t.State) || t.State == core.StateFailed {
				d, err := f.s.ctl.Get(ctx, t.ID)
				if err != nil {
					continue
				}
				out = append(out, gidStatus(f.gid.put(t.ID), d))
			}
		}
		return out, nil
	}
	tasks, _, err := f.s.ctl.List(ctx, core.TaskFilter{State: state})
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	out := make([]any, 0, len(tasks))
	for _, t := range tasks {
		if method == "tellActive" && (core.IsTerminal(t.State) || t.State == core.StatePaused) {
			continue
		}
		d, err := f.s.ctl.Get(ctx, t.ID)
		if err != nil {
			continue
		}
		out = append(out, gidStatus(f.gid.put(t.ID), d))
	}
	return out, nil
}

func (f *rpcFacade) getUris(ctx context.Context, params []json.RawMessage) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "missing GID"}
	}
	var gid string
	if json.Unmarshal(params[0], &gid) != nil {
		return nil, &rpcError{rpcErrParams, "invalid GID"}
	}
	id, ok := f.resolveGID(ctx, gid)
	if !ok {
		return nil, &rpcError{rpcErrAppBase, "GID not found"}
	}
	d, err := f.s.ctl.Get(ctx, id)
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	uris := make([]map[string]any, 0, 1)
	uris = append(uris, map[string]any{"uri": d.Input, "status": "used"})
	return uris, nil
}

func (f *rpcFacade) getFiles(ctx context.Context, params []json.RawMessage) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "missing GID"}
	}
	var gid string
	if json.Unmarshal(params[0], &gid) != nil {
		return nil, &rpcError{rpcErrParams, "invalid GID"}
	}
	id, ok := f.resolveGID(ctx, gid)
	if !ok {
		return nil, &rpcError{rpcErrAppBase, "GID not found"}
	}
	d, err := f.s.ctl.Get(ctx, id)
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	out := make([]map[string]any, 0)
	if d.Resource != nil {
		for i, rf := range d.Resource.Files {
			path := rf.Path
			if d.Options.Dir != "" && !strings.HasPrefix(path, "/") {
				path = strings.TrimRight(d.Options.Dir, "/") + "/" + path
			}
			sel := "false"
			if rf.Selected {
				sel = "true"
			}
			out = append(out, map[string]any{
				"index": strconv.Itoa(i + 1), "path": path, "length": strconv.FormatInt(rf.Size, 10),
				"completedLength": "0", "selected": sel, "uris": []any{},
			})
		}
	}
	return out, nil
}

func (f *rpcFacade) getPeers(ctx context.Context, params []json.RawMessage) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "missing GID"}
	}
	var gid string
	if json.Unmarshal(params[0], &gid) != nil {
		return nil, &rpcError{rpcErrParams, "invalid GID"}
	}
	id, ok := f.resolveGID(ctx, gid)
	if !ok {
		return nil, &rpcError{rpcErrAppBase, "GID not found"}
	}
	d, err := f.s.ctl.Get(ctx, id)
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	peers := make([]map[string]any, 0, len(d.Peers))
	for _, p := range d.Peers {
		peers = append(peers, map[string]any{"ip": p, "peerId": "", "downloadSpeed": "0", "uploadSpeed": "0"})
	}
	return peers, nil
}

func (f *rpcFacade) getServers(ctx context.Context, params []json.RawMessage) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "missing GID"}
	}
	var gid string
	if json.Unmarshal(params[0], &gid) != nil {
		return nil, &rpcError{rpcErrParams, "invalid GID"}
	}
	id, ok := f.resolveGID(ctx, gid)
	if !ok {
		return nil, &rpcError{rpcErrAppBase, "GID not found"}
	}
	d, err := f.s.ctl.Get(ctx, id)
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	servers := make([]map[string]any, 0, len(d.Trackers))
	for _, t := range d.Trackers {
		servers = append(servers, map[string]any{"uri": t, "downloadSpeed": "0"})
	}
	return servers, nil
}

func (f *rpcFacade) getGlobalStat(ctx context.Context) (any, *rpcError) {
	st, err := f.s.ctl.Stats(ctx)
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	return map[string]any{
		"downloadSpeed":   strconv.FormatInt(st.Rate, 10),
		"uploadSpeed":     "0",
		"numActive":       strconv.Itoa(st.Running),
		"numWaiting":      "0",
		"numStopped":      strconv.Itoa(st.Failed + st.Completed),
		"numStoppedTotal": strconv.Itoa(st.Tasks),
	}, nil
}

// getGlobalOption returns the option keys AriaNg's settings panel reads on
// connect. Real ud values are mapped where they exist (ADR-0031 compat subset);
// the rest are honest aria2-compatible defaults so the frontend renders and can
// round-trip changeGlobalOption. Unknown options remain accepted-but-ignored.
func (f *rpcFacade) getGlobalOption() (any, *rpcError) {
	g := config.Defaults().Global
	if f.s.cfg != nil {
		g = f.s.cfg.Get().Global
	}
	// Global rate fields are read only inside the config package (check_arch
	// guard 4: the Limiter is the sole rate authority). The mapping is done
	// there; this facade merely renders the result for AriaNg.
	downLimit, upLimit := g.Aria2GlobalLimits()
	return map[string]any{
		"dir":                        "", // ud has no global default dir; per-task
		"max-concurrent-downloads":   strconv.Itoa(g.MaxConcurrentTasks),
		"max-overall-download-limit": downLimit,
		"max-overall-upload-limit":   upLimit,
		"max-connection-per-server": strconv.Itoa(g.MaxConnectionsPerTask),
		"min-split-size":            g.ChunkSize,
		"split":                     strconv.Itoa(g.MaxConnectionsPerTask),
		"continue":                  "true",
		"file-allocation":           "none",
		"input-file":                "",
		"max-download-limit":        "0",
		"max-upload-limit":          "0",
		"auto-file-renaming":        "false",
		"remove-after-finished":     "",
		"bt-tracker":                "",
		"bt-max-peers":              "0",
		"rpc-listen-port":           "6800",
		"rpc-allow-origin-all":      "false",
		"rpc-save-upload-metadata":  "false",
	}, nil
}

func (f *rpcFacade) getOption(ctx context.Context, params []json.RawMessage) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "missing GID"}
	}
	var gid string
	if json.Unmarshal(params[0], &gid) != nil {
		return nil, &rpcError{rpcErrParams, "invalid GID"}
	}
	id, ok := f.resolveGID(ctx, gid)
	if !ok {
		return nil, &rpcError{rpcErrAppBase, "GID not found"}
	}
	d, err := f.s.ctl.Get(ctx, id)
	if err != nil {
		return nil, &rpcError{rpcErrAppBase, err.Error()}
	}
	return map[string]any{
		"dir":                  d.Options.Dir,
		"out":                  d.Options.Out,
		"max-download-limit":   d.Options.MaxDownloadLimit,
		"max-connection-per-server": strconv.Itoa(d.Options.MaxConnections),
	}, nil
}

func (f *rpcFacade) changeOption(ctx context.Context, params []json.RawMessage, method string) (any, *rpcError) {
	// changeGlobalOption has no GID (params[0] is the option object);
	// changeOption requires GID + options.
	need := 1
	if method == "changeOption" {
		need = 2
	}
	if len(params) < need {
		return nil, &rpcError{rpcErrParams, method + " requires " + strconv.Itoa(need) + " parameter(s)"}
	}
	var optJSON json.RawMessage
	if method == "changeOption" {
		var gid string
		if json.Unmarshal(params[0], &gid) != nil {
			return nil, &rpcError{rpcErrParams, "invalid GID"}
		}
		id, ok := f.resolveGID(ctx, gid)
		if !ok {
			return nil, &rpcError{rpcErrAppBase, "GID not found"}
		}
		optJSON = params[1]
		var opt core.TaskOptions
		applyRPCOptions(&opt, optJSON)
		if err := f.s.ctl.SetOption(ctx, id, opt); err != nil {
			return nil, &rpcError{rpcErrAppBase, err.Error()}
		}
	} else {
		optJSON = params[0]
		_ = optJSON // global options are accepted-but-ignored (no live global setter)
	}
	return "OK", nil
}

func (f *rpcFacade) multicall(ctx context.Context, params []json.RawMessage) (any, *rpcError) {
	if len(params) < 1 {
		return nil, &rpcError{rpcErrParams, "system.multicall requires an array"}
	}
	var calls []struct {
		MethodName string            `json:"methodName"`
		Params     []json.RawMessage `json:"params"`
	}
	if json.Unmarshal(params[0], &calls) != nil {
		return nil, &rpcError{rpcErrParams, "invalid multicall payload"}
	}
	out := make([]any, 0, len(calls))
	for _, c := range calls {
		res, rerr := f.call(ctx, c.MethodName, c.Params)
		if rerr != nil {
			out = append(out, map[string]any{"faultCode": rerr.Code, "faultString": rerr.Message})
		} else {
			out = append(out, []any{res})
		}
	}
	return out, nil
}

// applyRPCOptions translates aria2 options into TaskOptions. Unknown options
// are accepted but ignored (ADR-0019: returning an error breaks front-ends).
func applyRPCOptions(opt *core.TaskOptions, raw json.RawMessage) {
	var kv map[string]json.RawMessage
	if json.Unmarshal(raw, &kv) != nil {
		return
	}
	for k, v := range kv {
		switch k {
		case "dir":
			_ = json.Unmarshal(v, &opt.Dir)
		case "out":
			_ = json.Unmarshal(v, &opt.Out)
		case "max-download-limit":
			_ = json.Unmarshal(v, &opt.MaxDownloadLimit)
		case "max-connection-per-server", "split":
			var n int
			if json.Unmarshal(v, &n) == nil && n > 0 {
				opt.MaxConnections = n
			}
		}
		// select-file, header, cookie, all-proxy, bt-*: accepted but ignored
		// here; sensitive options must go through POST /credentials anyway.
	}
}

// gidStatus maps core.TaskDetail to the aria2 tellStatus structure. Bytes and
// speeds are strings per the aria2 wire format; values come only from core
// (ADR-0019: no Transport may compute its own byte counts).
func gidStatus(gid string, d *core.TaskDetail) map[string]any {
	left := d.BytesTotal - d.BytesDone
	if left < 0 {
		left = 0
	}
	total := strconv.FormatInt(d.BytesTotal, 10)
	completed := strconv.FormatInt(d.BytesDone, 10)
	return map[string]any{
		"gid":            gid,
		"status":         aria2Status(d.State),
		"totalLength":    total,
		"completedLength": completed,
		"uploadLength":   strconv.FormatInt(d.BytesUploaded, 10),
		"bitfield":       d.Bitfield, // HTTP tasks: empty; BT: from chunks (hex)
		"downloadSpeed":  strconv.FormatInt(d.Speed, 10),
		"uploadSpeed":    "0",
		"connections":    strconv.Itoa(d.Connections),
		"numPieces":      strconv.Itoa(d.NumPieces),
		"pieceLength":    strconv.FormatInt(d.PieceLength, 10),
		"dir":            d.Options.Dir,
		"files":          []any{},
		"errorCode":      "",
		"errorMessage":   d.ErrorMessage,
	}
}

// aria2Status compresses the 10-state core machine into aria2's 6 statuses.
func aria2Status(s core.TaskState) string {
	switch s {
	case core.StateCompleted, core.StateSeeding:
		return "complete"
	case core.StatePaused:
		return "paused"
	case core.StateFailed:
		return "error"
	case core.StateCanceled:
		return "removed"
	case core.StatePending, core.StateResolving, core.StateReady:
		return "waiting"
	default: // Running, PostProcessing
		return "active"
	}
}

