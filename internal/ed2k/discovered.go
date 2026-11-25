package goed2k

import (
	"net"
	"strings"
	"sync"
	"time"
)

// Endpoints learned from connected peers. eMule's "Update server list when a
// client connects" reads the server IP/port at the tail of OP_HELLOANSWER, and
// its Kad bootstrap reads the Kad UDP port from the CT_EMULE_UDPPORTS tag;
// both are then folded into the local lists. Our download engines are created
// per task while the long-lived search client owns server.met / nodes.dat, so
// the two sides meet in this small registry.
type discoveredEndpoint struct {
	Address string
	Source  string
	SeenAt  int64
}

const discoveredEndpointTTL = 30 * time.Minute

var discovered struct {
	sync.Mutex
	servers map[string]discoveredEndpoint
	kad     map[string]discoveredEndpoint
}

func init() {
	discovered.servers = make(map[string]discoveredEndpoint)
	discovered.kad = make(map[string]discoveredEndpoint)
}

// recordDiscoveredEndpoint stores a usable public endpoint. Local/broadcast
// addresses are rejected like eMule's IsGoodServerIP check.
func recordDiscoveredEndpoint(bucket map[string]discoveredEndpoint, address, source string) {
	address = strings.TrimSpace(address)
	if !usableDiscoveredAddress(address) {
		return
	}
	now := time.Now()
	discovered.Lock()
	defer discovered.Unlock()
	for key, item := range bucket {
		if now.Sub(time.UnixMilli(item.SeenAt)) > discoveredEndpointTTL {
			delete(bucket, key)
		}
	}
	if existing, ok := bucket[address]; ok {
		existing.SeenAt = now.UnixMilli()
		bucket[address] = existing
		return
	}
	bucket[address] = discoveredEndpoint{Address: address, Source: source, SeenAt: now.UnixMilli()}
}

func recordDiscoveredServer(address, source string) {
	recordDiscoveredEndpoint(discovered.servers, address, source)
}

func recordDiscoveredKadNode(address, source string) {
	recordDiscoveredEndpoint(discovered.kad, address, source)
}

// DiscoveredServers returns server endpoints learned from peers.
func DiscoveredServers() []string {
	return discoveredAddresses(discovered.servers)
}

// DiscoveredKadNodes returns Kad UDP endpoints learned from connected peers.
func DiscoveredKadNodes() []string {
	return discoveredAddresses(discovered.kad)
}

func discoveredAddresses(bucket map[string]discoveredEndpoint) []string {
	now := time.Now()
	discovered.Lock()
	defer discovered.Unlock()
	out := make([]string, 0, len(bucket))
	for key, item := range bucket {
		if now.Sub(time.UnixMilli(item.SeenAt)) > discoveredEndpointTTL {
			delete(bucket, key)
			continue
		}
		out = append(out, item.Address)
	}
	return out
}

func usableDiscoveredAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	ip, err := String2IP(host)
	if err != nil {
		return false
	}
	// string2IP stores the address in reversed-byte form.
	b := []byte{byte(ip), byte(ip >> 8), byte(ip >> 16), byte(ip >> 24)}
	if b[0] == 0 || b[0] == 127 || b[0] >= 224 {
		return false
	}
	if b[0] == 10 || (b[0] == 172 && b[1] >= 16 && b[1] <= 31) || (b[0] == 192 && b[1] == 168) {
		return false
	}
	return true
}
