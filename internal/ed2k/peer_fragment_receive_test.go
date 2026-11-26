package goed2k

import (
	"bytes"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/data"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// eMule limits a SendingPart frame to MAX_FRAGMENT_SIZE (10240 bytes), so a
// 190KiB block always arrives as 19 fragments. Treating the end of a fragment
// as the end of the block made CompleteBlock reject every real transfer
// (Received=10240 < DataSize=194560) and drop the peer with IOException, which
// left bytesDone stuck at 0 while the wire stayed busy.
func TestReceivePendingDataAccumulatesFragments(t *testing.T) {
	const (
		fragmentSize = 10240
		blockSize    = 190 * 1024
		totalSize    = blockSize * 3
	)
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("39F8BAB86C16143B3152807FBCA89B5E"),
		CreateTime: CurrentTime(),
		Size:       totalSize,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}

	pc := NewPeerConnection(session, protocol.Endpoint{}, transfer, nil)
	block := data.NewPieceBlock(0, 0)
	pc.downloadQueue = append(pc.downloadQueue, NewPendingBlock(block, totalSize))
	// Mirror RequestBlocks: the picker must own the block before the receive
	// path can mark it as writing, otherwise the write is skipped.
	transfer.picker.DownloadPiece(0)
	transfer.picker.MarkAsDownloading(block, nil)

	payload := bytes.Repeat([]byte{0x7B}, fragmentSize)
	for begin := int64(0); begin < blockSize; begin += fragmentSize {
		end := begin + fragmentSize
		req, err := data.MakePeerRequest(begin, end)
		if err != nil {
			t.Fatalf("MakePeerRequest(%d,%d): %v", begin, end, err)
		}
		pc.AppendIncoming(payload)
		pc.recvReq = req
		pc.recvPos = 0
		pc.transferringData = true
		pc.ReceivePendingData()

		if code := pc.DisconnectCode(); code == IOException {
			t.Fatalf("fragment %d..%d dropped the peer with IOException", begin, end)
		}
		if end < blockSize {
			if want := int64(end); pc.downloadQueue[0].Received != want {
				t.Fatalf("after %d..%d: Received = %d, want %d", begin, end, pc.downloadQueue[0].Received, want)
			}
			continue
		}
		// Last fragment: the accumulated block must complete and leave the
		// per-peer request queue.
		for _, pb := range pc.downloadQueue {
			if pb.Block.Compare(block) == 0 {
				t.Fatalf("block still pending after %d fragments (%d/%d bytes)",
					blockSize/fragmentSize, pb.Received, pb.DataSize)
			}
		}
	}
	if code := pc.DisconnectCode(); code == IOException {
		t.Fatal("completed block dropped the peer with IOException")
	}
	if n := len(session.diskTasks); n != 1 {
		t.Fatalf("disk write tasks = %d, want 1 (block never reached the writer)", n)
	}
}

// A fragment that no longer fits the pending buffer must not corrupt the piece
// silently: the block is released so another source can re-download it.
func TestReceivePendingDataRejectsOutOfRangeFragment(t *testing.T) {
	const blockSize = 190 * 1024
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("39F8BAB86C16143B3152807FBCA89B5E"),
		CreateTime: CurrentTime(),
		Size:       blockSize,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}

	pc := NewPeerConnection(session, protocol.Endpoint{}, transfer, nil)
	pc.downloadQueue = append(pc.downloadQueue, PendingBlock{
		Block:    data.NewPieceBlock(0, 0),
		DataSize: 4,
		Buffer:   make([]byte, 4),
	})
	req, err := data.MakePeerRequest(blockSize-10240, blockSize)
	if err != nil {
		t.Fatalf("MakePeerRequest: %v", err)
	}
	pc.AppendIncoming(bytes.Repeat([]byte{0x11}, 10240))
	pc.recvReq = req
	pc.transferringData = true
	pc.ReceivePendingData()

	if code := pc.DisconnectCode(); code != IOException {
		t.Fatalf("out-of-range fragment: code = %d, want IOException(%d)", code, IOException)
	}
}
