package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// aria2WSHub translates core.EventBus events into aria2 JSON-RPC notifications
// on WS /jsonrpc (ADR-0031, docs/07 §5.1 six notifications). It mirrors wsHub's
// drop-newest discipline so a slow AriaNg client never blocks the scheduler.
type aria2WSHub struct {
	bus      *core.EventBus
	upgrader websocket.Upgrader
	mu       sync.Mutex
	clients  map[*wsClient]bool
	once     sync.Once
}

func newAria2WSHub(bus *core.EventBus) *aria2WSHub {
	return &aria2WSHub{
		bus: bus,
		upgrader: websocket.Upgrader{
			ReadBufferSize: 1024, WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients: make(map[*wsClient]bool),
	}
}

func (h *aria2WSHub) start() {
	h.once.Do(func() { go h.run() })
}

func (h *aria2WSHub) run() {
	ch, _ := h.bus.Subscribe()
	for e := range ch {
		method, gid := aria2Notification(e)
		if method == "" {
			continue
		}
		frame, err := marshalAria2Notification(method, gid)
		if err != nil {
			continue
		}
		h.mu.Lock()
		for c := range h.clients {
			select {
			case c.send <- frame:
			default:
			}
		}
		h.mu.Unlock()
	}
}

func marshalAria2Notification(method, gid string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  []any{map[string]any{"gid": gid}},
	})
}

func (h *aria2WSHub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

func (h *aria2WSHub) remove(c *wsClient) {
	h.mu.Lock()
	if h.clients[c] {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// ServeHTTP upgrades a GET /jsonrpc to the aria2 notification stream. Auth is
// enforced by the caller (see Server.jsonRPCWS) via the token: query param or
// Bearer header before reaching here.
func (h *aria2WSHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsClient{conn: conn, send: make(chan []byte, 64)}
	h.add(c)
	defer func() {
		h.remove(c)
		_ = conn.Close()
	}()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	})
	go h.writer(c)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	}
}

func (h *aria2WSHub) writer(c *wsClient) {
	for frame := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
			return
		}
	}
}
