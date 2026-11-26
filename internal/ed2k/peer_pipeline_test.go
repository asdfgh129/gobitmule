package goed2k

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

// requestLog records, for every RequestParts the uploader receives, how many
// whole blocks it had already pushed. The old refill gate ("only ask again when
// the queue is empty") shows up as a rising count: the next request only arrives
// after the whole three-block batch is done, leaving the link idle in between.
// Incremental refill asks again right after the first block lands.
type requestLog struct {
	mu         sync.Mutex
	atRequest  []int
	sentBlocks int
}

func (r *requestLog) noteRequest() {
	r.mu.Lock()
	r.atRequest = append(r.atRequest, r.sentBlocks)
	r.mu.Unlock()
}

func (r *requestLog) noteBlockSent() {
	r.mu.Lock()
	r.sentBlocks++
	r.mu.Unlock()
}

func (r *requestLog) snapshot() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.atRequest...)
}

// pacedUploader answers like an eMule uploader and, crucially, keeps reading
// requests while it writes: a real client runs a socket loop, so it notices a
// refill as soon as it arrives. A single-threaded fake would only see the new
// request after finishing the whole batch and would hide the pipeline gap.
type pacedUploader struct {
	mu     sync.Mutex
	frames *e2eFrames
	hash   protocol.Hash
	log    *requestLog
	jobs   chan blockRange
}

type blockRange struct{ begin, end int64 }

func (u *pacedUploader) write(raw []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.frames.write(raw)
}

func (u *pacedUploader) pack(name string, packet protocol.Serializable) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.frames.comb.Pack(name, packet)
}

func (u *pacedUploader) packPayload(name string, packet protocol.Serializable, payload []byte) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	raw, _, err := u.frames.comb.PackPayload(name, packet, payload)
	return raw, err
}

// sender drains queued block ranges in order, one fragment at a time, at a pace
// slow enough that the client has time to react between blocks.
func (u *pacedUploader) sender(content []byte, fragmentPace time.Duration) {
	for job := range u.jobs {
		for off := job.begin; off < job.end; off += maxFragmentSize {
			chunkEnd := off + maxFragmentSize
			if chunkEnd > job.end {
				chunkEnd = job.end
			}
			part := clientproto.SendingPart32{
				Hash:        u.hash,
				BeginOffset: uint32(off),
				EndOffset:   uint32(chunkEnd),
			}
			raw, err := u.packPayload("client.SendingPart32", &part, content[off:chunkEnd])
			if err != nil {
				return
			}
			if u.write(raw) != nil {
				return
			}
			// Count the block as sent as soon as its last fragment is on the wire,
			// before the pacing sleep: the client reacts to that fragment while we
			// are still sleeping, and attributing the block to the sleep would make
			// the measurement lag by one block.
			if chunkEnd == job.end {
				u.log.noteBlockSent()
			}
			time.Sleep(fragmentPace)
		}
	}
}

func servePacedUploader(ln net.Listener, hash protocol.Hash, content []byte, pieces int,
	fragmentPace time.Duration, log *requestLog, stop chan struct{}) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	defer close(stop)
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	u := &pacedUploader{
		frames: &e2eFrames{conn: conn, comb: clientproto.NewPacketCombiner()},
		hash:   hash,
		log:    log,
		jobs:   make(chan blockRange, 32),
	}
	defer close(u.jobs)
	go u.sender(content, fragmentPace)

	available := protocol.NewBitField(pieces)
	available.SetAll()

	for {
		_, packet, err := u.frames.read()
		if err != nil {
			return
		}
		switch p := packet.(type) {
		case *clientproto.Hello:
			raw, err := u.pack("client.HelloAnswer", &clientproto.HelloAnswer{
				Hash:  protocol.MustHashFromString("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"),
				Point: protocol.NewEndpoint(0x043C0570, 4662),
				Properties: protocol.TagList{
					protocol.NewStringTag(0x01, "paced-uploader"),
					protocol.NewUInt32Tag(0x11, 0x3c),
				},
			})
			if err != nil {
				return
			}
			if u.write(raw) != nil {
				return
			}
		case *clientproto.FileRequest:
			raw, err := u.pack("client.FileAnswer", &clientproto.FileAnswer{
				Hash: hash,
				Name: protocol.ByteContainer16FromString("pipeline.bin"),
			})
			if err != nil {
				return
			}
			if u.write(raw) != nil {
				return
			}
		case *clientproto.FileStatusRequest:
			raw, err := u.pack("client.FileStatusAnswer", &clientproto.FileStatusAnswer{
				Hash:     hash,
				BitField: available,
			})
			if err != nil {
				return
			}
			if u.write(raw) != nil {
				return
			}
		case *clientproto.StartUpload:
			raw, err := u.pack("client.AcceptUpload", &clientproto.AcceptUpload{})
			if err != nil {
				return
			}
			if u.write(raw) != nil {
				return
			}
		case *clientproto.RequestParts32:
			log.noteRequest()
			for i := 0; i < p.CurrentFree; i++ {
				begin := int64(p.BeginOffset[i])
				end := int64(p.EndOffset[i])
				if end > int64(len(content)) {
					end = int64(len(content))
				}
				u.jobs <- blockRange{begin: begin, end: end}
			}
		}
	}
}

// The pipeline must stay full: the client has to ask for the next block as soon
// as one block lands, not after the whole batch drains. Waiting for an empty
// queue idled the connection for a round trip between batches, which is what
// made a source that eMule reads at 1.8 MB/s trickle here at ~100 KB/s.
func TestRequestBlocksRefillsBeforeQueueDrains(t *testing.T) {
	const blockSize = 190 * 1024
	// One block past the window, so there is exactly one refill to observe,
	// whatever the window is set to.
	const blocks = RequestQueueSize + 1
	const totalSize = blocks * blockSize
	content := make([]byte, totalSize)
	for i := range content {
		content[i] = byte(i*5 + 3)
	}
	hash := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	log := &requestLog{}
	stop := make(chan struct{})
	go servePacedUploader(ln, hash, content, 1, 6*time.Millisecond, log, stop)

	settings := NewSettings()
	settings.PeerConnectionTimeout = 10
	session := NewSession(settings)
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       hash,
		CreateTime: CurrentTime(),
		Size:       totalSize,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	endpoint := protocol.EndpointFromInet(ln.Addr().(*net.TCPAddr))
	peer := NewPeerWithSourceAndHash(endpoint, true, int(PeerServer), protocol.Invalid)
	if _, err := transfer.ConnectToPeer(&peer); err != nil {
		t.Fatalf("ConnectToPeer: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		session.PumpIO()
		if len(session.diskTasks) >= blocks {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if got := len(session.diskTasks); got != blocks {
		t.Fatalf("queued %d of %d blocks for writing", got, blocks)
	}
	for _, task := range session.diskTasks {
		write, ok := task.callable.(*AsyncWrite)
		if !ok {
			t.Fatalf("unexpected disk task %T", task.callable)
		}
		r := write.Block.Range(totalSize)
		if !bytes.Equal(write.Buffer, content[r.Left:r.Right]) {
			t.Fatalf("block %d payload mismatch", write.Block.PieceBlock)
		}
	}

	requests := log.snapshot()
	if len(requests) < 2 {
		t.Fatalf("uploader saw %d RequestParts; expected a refill mid-batch: %v", len(requests), requests)
	}
	// The window is three blocks, so the last request is the one that fetches the
	// fourth. Under the old gates it could only be sent after the whole batch had
	// been pushed (sentBlocks == RequestQueueSize); incremental refill sends it as
	// soon as the first block lands.
	last := requests[len(requests)-1]
	if last >= RequestQueueSize {
		t.Fatalf("the fourth block was only requested after %d blocks were fully sent (window %d): the queue was allowed to drain, %v",
			last, RequestQueueSize, requests)
	}
	if requests[0] != 0 {
		t.Fatalf("first request logged at sentBlocks=%d, want 0 (it carries the whole window)", requests[0])
	}
}

// A source that hands nothing over must not be closed while blocks are still in
// flight: the refill rework asks for "free slots", which is zero when a batch is
// fully queued.
func TestRequestBlocksDoesNotCloseWithPendingWindow(t *testing.T) {
	const blockSize = 190 * 1024
	const totalSize = 8 * blockSize
	hash := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")

	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       hash,
		CreateTime: CurrentTime(),
		Size:       totalSize,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	pc := &PeerConnection{session: session, transfer: transfer}
	// Simulate a full window with no socket: the request path must decide there is
	// nothing to do instead of asking the picker for a negative count.
	pc.downloadQueue = make([]PendingBlock, RequestQueueSize)
	pc.RequestBlocks()
	if pc.IsDisconnecting() {
		t.Fatal("RequestBlocks closed the connection while a full window was in flight")
	}
	if len(pc.downloadQueue) != RequestQueueSize {
		t.Fatalf("downloadQueue = %d, want %d", len(pc.downloadQueue), RequestQueueSize)
	}
}
