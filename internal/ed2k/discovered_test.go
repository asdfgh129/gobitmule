package goed2k

import (
	"testing"
	"time"
)

func TestUsableDiscoveredAddress(t *testing.T) {
	cases := []struct {
		address string
		want    bool
	}{
		{"1.2.3.4:4661", true},
		{"37.119.55.104:4662", true},
		{"127.0.0.1:4661", false},
		{"0.0.0.0:4661", false},
		{"10.1.2.3:4661", false},
		{"172.16.0.1:4661", false},
		{"172.31.255.254:4661", false},
		{"192.168.1.1:4661", false},
		{"224.0.0.1:4661", false},
		{"not-an-ip:4661", false},
		{"1.2.3.4", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := usableDiscoveredAddress(tc.address); got != tc.want {
			t.Errorf("usableDiscoveredAddress(%q) = %v, want %v", tc.address, got, tc.want)
		}
	}
}

func TestRecordDiscoveredServerDeduplicates(t *testing.T) {
	discovered.Lock()
	discovered.servers = make(map[string]discoveredEndpoint)
	discovered.Unlock()

	recordDiscoveredServer("1.2.3.4:4661", "peer")
	recordDiscoveredServer("1.2.3.4:4661", "peer")
	recordDiscoveredServer("127.0.0.1:4661", "peer")

	got := DiscoveredServers()
	if len(got) != 1 || got[0] != "1.2.3.4:4661" {
		t.Fatalf("DiscoveredServers() = %v, want one public endpoint", got)
	}
}

func TestDiscoveredAddressesExpire(t *testing.T) {
	now := time.Now()
	bucket := map[string]discoveredEndpoint{
		"fresh": {Address: "1.2.3.4:4661", SeenAt: now.UnixMilli()},
		"stale": {Address: "5.6.7.8:4661", SeenAt: now.Add(-2 * discoveredEndpointTTL).UnixMilli()},
	}
	got := discoveredAddresses(bucket)
	if len(got) != 1 || got[0] != "1.2.3.4:4661" {
		t.Fatalf("discoveredAddresses() = %v, want only the fresh endpoint", got)
	}
	if _, ok := bucket["stale"]; ok {
		t.Fatal("expired endpoint must be removed from the bucket")
	}
}
