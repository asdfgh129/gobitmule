package kad

import "testing"

// KADEMLIA_CALLBACK_REQ is 34 bytes on the wire: eMule rejects a bare port.
func TestCallbackReqWireFormat(t *testing.T) {
	req := CallbackReq{TCPPort: 4662}
	req.Check[0], req.Check[15] = 0x11, 0x22
	req.File[0], req.File[15] = 0x33, 0x44
	raw, err := req.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if len(raw) != 2+34 {
		t.Fatalf("packet len=%d want 36", len(raw))
	}
	var got CallbackReq
	if err := got.Unpack(raw[2:]); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if got.TCPPort != 4662 || got.Check[0] != 0x11 || got.Check[15] != 0x22 ||
		got.File[0] != 0x33 || got.File[15] != 0x44 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := got.Unpack(make([]byte, 2)); err == nil {
		t.Fatal("accepted a short callback request")
	}
}

func TestFindBuddyWireFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		pack func(uint16) ([]byte, error)
	}{
		{"req", func(p uint16) ([]byte, error) { return FindBuddyReq{TCPPort: p}.Pack() }},
		{"res", func(p uint16) ([]byte, error) { return FindBuddyRes{TCPPort: p}.Pack() }},
	} {
		raw, err := tc.pack(4662)
		if err != nil {
			t.Fatalf("%s pack: %v", tc.name, err)
		}
		if len(raw) != 4 {
			t.Fatalf("%s len=%d want 4", tc.name, len(raw))
		}
	}
}
