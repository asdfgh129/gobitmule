package client

import (
	"bytes"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func TestCallbackRoundTrip(t *testing.T) {
	packet := Callback{
		Check: protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		File:  protocol.MustHashFromString("C7A91B4E8D2F3A0B5E6D7C8091A2B3C4"),
		IP:    0x0100007F,
		Port:  4662,
	}
	var buf bytes.Buffer
	if err := packet.Put(&buf); err != nil {
		t.Fatalf("put: %v", err)
	}
	if buf.Len() != 38 {
		t.Fatalf("wire len=%d want 38", buf.Len())
	}
	var got Callback
	if err := got.Get(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Check != packet.Check || got.File != packet.File || got.IP != packet.IP || got.Port != packet.Port {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Endpoint().Port() != 4662 {
		t.Fatalf("endpoint=%s", got.Endpoint().String())
	}
}
