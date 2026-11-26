package goed2k

import (
	"bytes"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/data"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

// A real eMule peer compresses a whole 190KiB block into one CompressedPart
// frame and lets TCP split it: the frame header arrives first, then the
// compressed payload over several reads. The receiver must reconstruct the
// compressed payload from the leftover stream bytes, decompress it, and hand the
// raw block to the writer. This is the exact shape observed on the wire
// ("<- CompressedPart32 0 len=25888") that used to end in "disconnect code=11".
func TestCompressedPartCompletesBlockAcrossReads(t *testing.T) {
	const totalSize = 190 * 1024
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(),
		Size:       totalSize,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}

	pc := NewPeerConnection(session, protocol.Endpoint{}, transfer, nil)
	block := data.NewPieceBlock(0, 0)
	pc.downloadQueue = append(pc.downloadQueue, NewPendingBlock(block, totalSize))
	transfer.picker.DownloadPiece(0)
	transfer.picker.MarkAsDownloading(block, nil)

	raw := bytes.Repeat([]byte{0x2C}, totalSize)
	zipped := compressForTest(t, raw)

	part := clientproto.CompressedPart32{
		Hash:             transfer.GetHash(),
		BeginOffset:      0,
		CompressedLength: uint32(len(zipped)),
	}
	frame, _, err := pc.combiner.PackPayload("client.CompressedPart32", &part, zipped)
	if err != nil {
		t.Fatalf("pack compressed part: %v", err)
	}

	// Feed the frame in 16KiB reads, exactly like Connection.DoRead.
	for off := 0; off < len(frame); off += 16 * 1024 {
		end := off + 16*1024
		if end > len(frame) {
			end = len(frame)
		}
		pc.AppendIncoming(frame[off:end])
		if pc.transferringData {
			pc.ReceivePendingData()
		} else if err := pc.ProcessIncoming(); err != nil {
			t.Fatalf("ProcessIncoming at %d: %v", off, err)
		}
	}

	if code := pc.DisconnectCode(); code == IOException {
		t.Fatalf("compressed block was rejected with IOException (received=%d want=%d)",
			pc.downloadQueue[0].Received, pc.downloadQueue[0].DataSize)
	}
	if n := len(session.diskTasks); n != 1 {
		t.Fatalf("disk write tasks = %d, want 1 (compressed block never reached the writer)", n)
	}
	task := session.diskTasks[0]
	if task.transfer != transfer {
		t.Fatal("disk task was queued for the wrong transfer")
	}
}
