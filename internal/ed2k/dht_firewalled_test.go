package goed2k

import (
	"net"
	"testing"
)

// TestFirewalledFromExternalIPsWildcardBind locks in the NAT case that broke
// the eMule-style reachable indicator: net.ListenUDP on 0.0.0.0 reports a
// wildcard bind IP, so two non-zero, equal external reports must mean the
// client is reachable rather than firewalled.
func TestFirewalledFromExternalIPsWildcardBind(t *testing.T) {
	ipA := protocolIP(net.ParseIP("112.5.60.4"))
	ipB := protocolIP(net.ParseIP("112.5.32.34"))

	tests := []struct {
		name string
		ips  []uint32
		want bool
	}{
		{"not enough reports", []uint32{ipA}, true},
		{"two equal reports behind nat", []uint32{ipA, ipA}, false},
		{"two different reports", []uint32{ipA, ipB}, true},
		{"zero first report", []uint32{0, ipA}, true},
		{"zero second report", []uint32{ipA, 0}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := &DHTTracker{externalIPs: append([]uint32(nil), tt.ips...)}
			if got := tracker.firewalledFromExternalIPsLocked(); got != tt.want {
				t.Fatalf("firewalled=%v, want %v", got, tt.want)
			}
		})
	}
}

// TestFirewalledFromExternalIPsConcreteBind covers engines bound to a specific
// interface: both reports must match the bind IP to count as reachable.
func TestFirewalledFromExternalIPsConcreteBind(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Skipf("cannot bind loopback UDP: %v", err)
	}
	defer conn.Close()
	localIP := protocolIP(net.ParseIP("127.0.0.1"))
	external := protocolIP(net.ParseIP("112.5.60.4"))

	tracker := &DHTTracker{conn: conn, externalIPs: []uint32{localIP, localIP}}
	if tracker.firewalledFromExternalIPsLocked() {
		t.Fatal("two reports matching concrete bind IP should be reachable")
	}
	tracker.externalIPs = []uint32{localIP, external}
	if !tracker.firewalledFromExternalIPsLocked() {
		t.Fatal("report differing from concrete bind IP should be firewalled")
	}
}
