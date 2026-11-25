package goed2k

import (
	"bytes"
	"compress/zlib"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/data"
)

// Build a pending block the way ReceivePendingData does: Buffer is allocated
// at the full raw capacity of the piece block, and the (possibly compressed)
// bytes are copied into the front while Received counts the incoming bytes.
func newTestPendingBlock(rawSize int, wire []byte) *PendingBlock {
	buf := make([]byte, rawSize)
	copy(buf, wire)
	return &PendingBlock{
		Block:    data.NewPieceBlock(0, 0),
		DataSize: int64(len(wire)),
		Received: int64(len(wire)),
		Buffer:   buf,
	}
}

func compressForTest(t *testing.T, payload []byte) []byte {
	t.Helper()
	var zipped bytes.Buffer
	zw := zlib.NewWriter(&zipped)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close compressor: %v", err)
	}
	return zipped.Bytes()
}

// A compressed block must be decompressed before it is written to the piece
// manager. A zlib failure used to fall through silently, letting the raw
// compressed bytes be written as file data and never re-requested.
func TestCompleteBlockDecompressesValidPayload(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5A}, 4096)
	zipped := compressForTest(t, payload)

	p := &PeerConnection{recvReqCompressed: true}
	pb := newTestPendingBlock(len(payload), zipped)
	if !p.CompleteBlock(pb) {
		t.Fatal("valid compressed block was rejected")
	}
	if !bytes.Equal(pb.Buffer[:len(payload)], payload) {
		t.Fatalf("decompressed payload mismatch: got %d bytes want %d", len(pb.Buffer), len(payload))
	}
}

func TestCompleteBlockRejectsCorruptCompressedPayload(t *testing.T) {
	wire := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x01, 0x02, 0x03}
	p := &PeerConnection{recvReqCompressed: true}
	pb := newTestPendingBlock(4096, wire)
	if p.CompleteBlock(pb) {
		t.Fatal("corrupt compressed block was accepted")
	}
	// Buffer must remain untouched so the caller can drop the block and
	// re-request it from another source instead of writing garbage.
	for i, b := range wire {
		if pb.Buffer[i] != b {
			t.Fatalf("rejected buffer was mutated at %d: got %02X want %02X", i, pb.Buffer[i], b)
		}
	}
}

func TestCompleteBlockRejectsSizeMismatch(t *testing.T) {
	// Compress a short payload but declare a full-size block: the decompressed
	// length will not match the pending block capacity.
	payload := bytes.Repeat([]byte{0x11}, 128)
	zipped := compressForTest(t, payload)

	p := &PeerConnection{recvReqCompressed: true}
	pb := newTestPendingBlock(4096, zipped)
	if p.CompleteBlock(pb) {
		t.Fatal("compressed block with size mismatch was accepted")
	}
}

func TestCompleteBlockIncompleteAndPlaintext(t *testing.T) {
	// Incomplete blocks are never completed.
	pb := &PendingBlock{DataSize: 100, Received: 50, Buffer: make([]byte, 100)}
	if p := (&PeerConnection{recvReqCompressed: true}); p.CompleteBlock(pb) {
		t.Fatal("incomplete block was accepted")
	}

	// Plaintext blocks need no decompression.
	pb = newTestPendingBlock(4, []byte{1, 2, 3, 4})
	if p := (&PeerConnection{}); !p.CompleteBlock(pb) {
		t.Fatal("complete plaintext block was rejected")
	}
}
