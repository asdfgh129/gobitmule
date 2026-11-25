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
