package goed2k

import (
	"net"
	"testing"

	kadproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/kad"
)

func TestKadEndpointIPConversion(t *testing.T) {
	ip := net.ParseIP("79.117.19.23")
	endpoint := kadproto.Endpoint{IP: protocolIP(ip), UDPPort: 4672}
	addr := udpAddrFromKad(endpoint)
	if got, want := addr.String(), "79.117.19.23:4672"; got != want {
		t.Fatalf("udpAddrFromKad() = %q, want %q", got, want)
	}
	if got := protocolIP(addr.IP); got != endpoint.IP {
		t.Fatalf("protocolIP round trip = %#x, want %#x", got, endpoint.IP)
	}
}

func TestNodesDatEndpointIPConversion(t *testing.T) {
	// Minimal version-2 nodes.dat with one contact. The IP bytes are the
	// little-endian encoding of 79.117.19.23 (0x4F751317), as written by eMule.
	raw := []byte{
		0x00, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00,
		0x44, 0xf9, 0x0b, 0xff, 0xf5, 0x4b, 0xea, 0x15,
		0x65, 0xab, 0xa9, 0x5f, 0xcd, 0x0c, 0xfe, 0x8e,
		0x17, 0x13, 0x75, 0x4f,
		0x40, 0x12,
		0x36, 0x12,
		0x08,
		0x82, 0x2d, 0xfd, 0x87, 0x70, 0x05, 0x3c, 0x04,
		0x01,
	}
	nodes, err := kadproto.ParseNodesDat(raw)
	if err != nil {
		t.Fatalf("ParseNodesDat() error = %v", err)
	}
	if len(nodes.Contacts) != 1 {
		t.Fatalf("contacts = %d, want 1", len(nodes.Contacts))
	}
	addr := udpAddrFromKad(nodes.Contacts[0].Endpoint)
	if got, want := addr.String(), "79.117.19.23:4672"; got != want {
		t.Fatalf("contact address = %q, want %q", got, want)
	}
}
