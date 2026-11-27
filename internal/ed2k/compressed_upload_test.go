package goed2k

import (
	"bytes"
	"compress/zlib"
	"io"
	"math/rand"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// #13: a peer that announced DataCompVer gets the block zlib-compressed as
// CompressedPart frames. The frames carry the block start offset and the total
// compressed length; inflating their concatenated payload restores the block.
func TestSendCompressedPartRoundTrip(t *testing.T) {
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(),
		Size:       300 * 1024,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	pc := NewPeerConnection(session, protocol.Endpoint{}, transfer, nil)
	pc.remotePeerInfo.Misc1.DataCompVer = 1

	// Highly repetitive bytes compress to well under 10 KiB (a single frame).
	// Repeat a pseudo-random 24 KiB seed so the compressed form stays above
	// 10 KiB and the sender actually exercises its fragmentation path.
	seed := make([]byte, 24*1024)
	rng := rand.New(rand.NewSource(7))
	rng.Read(seed)
	raw := make([]byte, 0, 192*1024)
	for i := 0; i < 8; i++ {
		raw = append(raw, seed...)
	}
	if !pc.SendCompressedPart(0, raw) {
		t.Fatal("SendCompressedPart returned false for compressible block")
	}
	packets := pc.PendingPackets()
	if len(packets) < 2 {
		t.Fatalf("compressed block should fragment into multiple frames, got %d", len(packets))
	}
	var payload bytes.Buffer
	for _, frame := range packets {
		// 6-byte packet header + 24-byte CompressedPart32 body (hash/offset/len).
		if len(frame) < 30 {
			t.Fatalf("short frame %d", len(frame))
		}
		// EMuleProt 0xC5 + CompressedPart32 0x40.
		if frame[0] != 0xC5 || frame[5] != 0x40 {
			t.Fatalf("unexpected frame header % X (want 0xC5..../opcode 0x40)", frame[:6])
		}
		payload.Write(frame[30:])
	}
	zr, err := zlib.NewReader(bytes.NewReader(payload.Bytes()))
	if err != nil {
		t.Fatalf("zlib reader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("inflated block differs: got %d bytes want %d", len(got), len(raw))
	}
}

// When compression does not pay off (incompressible data) the sender must not
// emit CompressedPart frames so the caller falls back to plaintext SendingPart.
func TestSendCompressedPartFallsBackOnIncompressibleData(t *testing.T) {
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(),
		Size:       100 * 1024,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	pc := NewPeerConnection(session, protocol.Endpoint{}, transfer, nil)
	pc.remotePeerInfo.Misc1.DataCompVer = 1

	raw := make([]byte, 20*1024)
	rng := rand.New(rand.NewSource(42))
	rng.Read(raw)
	if pc.SendCompressedPart(0, raw) {
		t.Fatal("SendCompressedPart returned true for incompressible block")
	}
	if n := len(pc.PendingPackets()); n != 0 {
		t.Fatalf("no frames should be queued on fallback, got %d", n)
	}
}

// A peer that did not announce DataCompVer must never receive compressed data.
func TestSendCompressedPartRespectsPeerCapability(t *testing.T) {
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(),
		Size:       100 * 1024,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	pc := NewPeerConnection(session, protocol.Endpoint{}, transfer, nil)
	// DataCompVer left at zero.
	if pc.SendCompressedPart(0, bytes.Repeat([]byte{0x11}, 20*1024)) {
		t.Fatal("SendCompressedPart returned true without peer compression support")
	}
}
