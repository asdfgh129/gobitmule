package goed2k

import "sync"

// Active engine clients. The Web console's task-detail source table needs
// peer snapshots from per-task download engines, which are created and torn
// down independently of the long-lived search client. Registering them here
// lets the API aggregate live sources without importing the transport layer.
var activeClients struct {
	sync.RWMutex
	clients map[*Client]struct{}
}

func init() {
	activeClients.clients = make(map[*Client]struct{})
}

func RegisterActiveClient(c *Client) {
	if c == nil {
		return
	}
	activeClients.Lock()
	activeClients.clients[c] = struct{}{}
	activeClients.Unlock()
}

func UnregisterActiveClient(c *Client) {
	if c == nil {
		return
	}
	activeClients.Lock()
	delete(activeClients.clients, c)
	activeClients.Unlock()
}

// ActiveNetworkSnapshot reports the server/KAD state of the per-task download
// engines. The Web console's server and KAD pages must reflect the engine that
// actually owns the configured 4662/4672 (or user-replaced) ports: the
// long-lived search client deliberately binds port+1 and would otherwise show
// a permanently firewalled LowID even when the download engine is reachable.
type ActiveNetworkSnapshot struct {
	HasEngine bool
	Servers   []ServerConnectionInfo
	KadStatus *DHTStatus
	KadState  *ClientDHTState
}

func ActiveNetworkStatus() ActiveNetworkSnapshot {
	activeClients.RLock()
	clients := make([]*Client, 0, len(activeClients.clients))
	for c := range activeClients.clients {
		clients = append(clients, c)
	}
	activeClients.RUnlock()

	out := ActiveNetworkSnapshot{}
	seen := make(map[string]struct{})
	for _, c := range clients {
		if c == nil || c.Session() == nil {
			continue
		}
		out.HasEngine = true
		for _, server := range c.Session().ServerSnapshot() {
			key := server.Address + "|" + server.Identifier
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out.Servers = append(out.Servers, server)
		}
		if tracker := c.GetDHTTracker(); tracker != nil {
			status := tracker.Status()
			out.KadStatus = &status
			if state := tracker.SnapshotState(); state != nil {
				out.KadState = state
			}
		}
		if len(out.Servers) > 0 && out.KadStatus != nil {
			break
		}
	}
	return out
}

// ActivePeerStatuses returns all peers currently attached to any registered
// engine, deduplicated by transfer hash + endpoint.
func ActivePeerStatuses() []ClientPeerSnapshot {
	activeClients.RLock()
	clients := make([]*Client, 0, len(activeClients.clients))
	for c := range activeClients.clients {
		clients = append(clients, c)
	}
	activeClients.RUnlock()

	out := make([]ClientPeerSnapshot, 0)
	seen := make(map[string]struct{})
	for _, c := range clients {
		for _, p := range c.PeerStatuses() {
			key := p.TransferHash.String() + "|" + p.Peer.Endpoint.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}
