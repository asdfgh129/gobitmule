package server

import (
	"bytes"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// OP_SERVERIDENT must surface ST_UDPFLAGS/ST_UDPKEY/ST_UDPKEYIP, otherwise the
// session can neither pick OP_GLOBGETSOURCES2 nor enable the RC4 envelope.
func TestServerIdentDecodesUDPCapabilities(t *testing.T) {
	hash := protocol.MustHashFromString("26DFB3060428ACEce9ec8864de7126ae")
	var body bytes.Buffer
	_ = protocol.WriteHash(&body, hash)
	_ = protocol.WriteUInt32(&body, 0x0100007F)
	_ = protocol.WriteUInt16(&body, 4661)
	tags := protocol.TagList{
		{Type: protocol.TagTypeString, ID: TagServerName, String: "test server"},
		{Type: protocol.TagTypeUint32, ID: TagUDPFlags, UInt32: UDPFlagExtGetSources | UDPFlagExtGetSources2 | UDPFlagUDPObfuscation},
		{Type: protocol.TagTypeUint32, ID: TagUDPKey, UInt32: 0xDEADBEEF},
		{Type: protocol.TagTypeUint32, ID: TagUDPKeyIP, UInt32: 0x0100007F},
	}
	if err := tags.Put(&body); err != nil {
		t.Fatalf("put tags: %v", err)
	}

	var ident ServerIdent
	if err := ident.Get(bytes.NewReader(body.Bytes())); err != nil {
		t.Fatalf("get: %v", err)
	}
	if ident.Hash != hash || ident.Port != 4661 {
		t.Fatalf("header mismatch: %+v", ident)
	}
	if ident.Name != "test server" {
		t.Fatalf("name=%q", ident.Name)
	}
	if ident.UDPKey != 0xDEADBEEF || ident.UDPKeyIP != 0x0100007F {
		t.Fatalf("udp key=%x ip=%x", ident.UDPKey, ident.UDPKeyIP)
	}
	if !ident.SupportsUDPGetSources2() || !ident.SupportsUDPObfuscation() {
		t.Fatalf("capabilities not decoded: %x", ident.UDPFlags)
	}
}

// A datagram may repeat <E3 9B> before every record; the parser has to consume
// the repeated header instead of treating it as a hash.
func TestUDPFoundSourcesMultipleSubpackets(t *testing.T) {
	h1 := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")
	h2 := protocol.MustHashFromString("C7A91B4E8D2F3A0B5E6D7C8091A2B3C4")
	record := func(h protocol.Hash, ip int32, port uint16) []byte {
		var body bytes.Buffer
		_ = protocol.WriteHash(&body, h)
		_ = body.WriteByte(1)
		_ = protocol.WriteInt32(&body, ip)
		_ = protocol.WriteUInt16(&body, port)
		return packUDPDatagram(OpGlobFoundSources, body.Bytes())
	}
	datagram := append(record(h1, 0x0100007F, 4662), record(h2, 0x0200007F, 4663)...)
	got := ParseUDPFoundSources(datagram)
	if len(got[h1]) != 1 || len(got[h2]) != 1 {
		t.Fatalf("parsed=%v", got)
	}
	if got[h2][0].Port() != 4663 {
		t.Fatalf("h2 endpoint=%s", got[h2][0].String())
	}
}

// The parser must not scan for a header inside a payload.
func TestUDPParserRejectsEmbeddedHeader(t *testing.T) {
	// Plain payload that starts with a byte pair equal to <E3 9B> but has no
	// leading protocol header: it is not a valid datagram.
	if _, ok := stripUDPDatagram([]byte{0x00, 0xE3, 0x9B, 0x01}, OpGlobFoundSources); ok {
		t.Fatal("accepted a datagram without a leading header")
	}
}

func TestUDPGlobGetSourcesLegacy(t *testing.T) {
	h := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")
	datagram := UDPGlobGetSourcesLegacy(h)
	if datagram[1] != OpGlobGetSources {
		t.Fatalf("opcode=0x%02x want 0x%02x", datagram[1], OpGlobGetSources)
	}
	if len(datagram) != 2+16 {
		t.Fatalf("len=%d want 18", len(datagram))
	}
}
