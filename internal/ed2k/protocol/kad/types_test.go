package kad

import (
	"bytes"
	"compress/zlib"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func TestDecodePacketPlainAndPacked(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}

	opcode, got, err := DecodePacket(append([]byte{ProtocolHeader, SearchResOp}, payload...))
	if err != nil {
		t.Fatalf("plain DecodePacket() error = %v", err)
	}
	if opcode != SearchResOp || !bytes.Equal(got, payload) {
		t.Fatalf("plain DecodePacket() = %#x, %x; want %#x, %x", opcode, got, SearchResOp, payload)
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	packed := append([]byte{PackedProtocolHeader, SearchResOp}, compressed.Bytes()...)
	opcode, got, err = DecodePacket(packed)
	if err != nil {
		t.Fatalf("packed DecodePacket() error = %v", err)
	}
	if opcode != SearchResOp || !bytes.Equal(got, payload) {
		t.Fatalf("packed DecodePacket() = %#x, %x; want %#x, %x", opcode, got, SearchResOp, payload)
	}
}

func TestTagGetNamedStringTag(t *testing.T) {
	var wire bytes.Buffer
	name := "filename"
	value := "hello.bin"
	if err := wire.WriteByte(TagTypeString); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteUInt16(&wire, uint16(len(name))); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.WriteString(name); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteUInt16(&wire, uint16(len(value))); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.WriteString(value); err != nil {
		t.Fatal(err)
	}

	var tag Tag
	if err := tag.Get(bytes.NewReader(wire.Bytes())); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if tag.Name != name {
		t.Fatalf("Name = %q, want %q", tag.Name, name)
	}
	if tag.ID != 0 {
		t.Fatalf("ID = %#x, want 0", tag.ID)
	}
	if tag.String != value {
		t.Fatalf("String = %q, want %q", tag.String, value)
	}
}

func TestTagGetNamedSingleCharacterTagUsesNumericID(t *testing.T) {
	var wire bytes.Buffer
	if err := wire.WriteByte(TagTypeUint32); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteUInt16(&wire, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.WriteString("X"); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteUInt32(&wire, 0x12345678); err != nil {
		t.Fatal(err)
	}

	var tag Tag
	if err := tag.Get(bytes.NewReader(wire.Bytes())); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if tag.ID != 'X' {
		t.Fatalf("ID = %#x, want %#x", tag.ID, byte('X'))
	}
	if tag.Name != "" {
		t.Fatalf("Name = %q, want empty", tag.Name)
	}
	if tag.UInt64 != 0x12345678 {
		t.Fatalf("UInt64 = %#x, want %#x", tag.UInt64, uint64(0x12345678))
	}
}

func TestTagRoundTrip(t *testing.T) {
	tests := []Tag{
		{Type: TagTypeUint16, ID: TagSourcePort, UInt64: 4662},
		{Type: TagTypeUint32, Name: "kad", UInt64: 0x0A0B0C0D},
		{Type: TagTypeString, Name: "filename", String: "windows.iso"},
		{Type: TagTypeHash16, ID: 0x01, Bytes: bytes.Repeat([]byte{0xAB}, 16)},
		{Type: TagTypeFloat32, ID: 0x02, UInt64: 0x12345678},
		{Type: TagTypeBool, ID: 0x03, UInt64: 1},
		{Type: TagTypeBlob, ID: 0x04, Bytes: []byte("blob")},
		{Type: TagTypeBsob, ID: 0x05, Bytes: []byte{1, 2, 3}},
	}
	for _, want := range tests {
		var wire bytes.Buffer
		if err := want.Put(&wire); err != nil {
			t.Fatalf("Put(%+v) error = %v", want, err)
		}
		var got Tag
		if err := got.Get(bytes.NewReader(wire.Bytes())); err != nil {
			t.Fatalf("Get(%+v) error = %v", want, err)
		}
		if got.Type != want.Type || got.ID != want.ID || got.Name != want.Name ||
			got.UInt64 != want.UInt64 || got.String != want.String ||
			!bytes.Equal(got.Bytes, want.Bytes) {
			t.Fatalf("round trip = %+v, want %+v", got, want)
		}
	}
}
