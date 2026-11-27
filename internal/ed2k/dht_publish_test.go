package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// #14: only a high ID is a routable IP that may be advertised to KAD. A low ID
// is a server token; publishing it would poison the network with a bogus source.
func TestPublishableSourceEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		id       int32
		port     int
		wantOK   bool
		wantIP   int32
		wantPort int
	}{
		{"zero id", 0, 4662, false, 0, 0},
		{"low id", 0x000A0001, 4662, false, 0, 0},
		{"high id", int32(0x75010001), 4662, true, int32(0x75010001), 4662},
		{"high id no port", int32(0x75010001), 0, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, ok := publishableSourceEndpoint(tc.id, tc.port)
			if ok != tc.wantOK {
				t.Fatalf("ok=%t want %t (id=%08x)", ok, tc.wantOK, uint32(tc.id))
			}
			if ok && (ep.IP() != tc.wantIP || ep.Port() != tc.wantPort) {
				t.Fatalf("endpoint=%s want ip=%08x port=%d", ep, uint32(tc.wantIP), tc.wantPort)
			}
		})
	}
}

// With no DHT tracker the publish is a silent no-op even for a valid high ID.
func TestPublishTransferToDHTWithoutTracker(t *testing.T) {
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(),
		Size:       1024,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	session.clientID = int32(0x75010001)
	if session.PublishTransferToDHT(transfer) {
		t.Fatal("publish succeeded without a KAD tracker")
	}
}
