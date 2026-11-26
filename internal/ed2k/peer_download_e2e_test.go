package goed2k

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

// maxFragmentSize is eMule's MAX_FRAGMENT_SIZE: one SendingPart frame never
// carries more than 10240 bytes, whatever the size of the requested block.
const maxFragmentSize = 10240

type e2eFrames struct {
	conn net.Conn
	comb protocol.PacketCombiner
}

func (f *e2eFrames) read() (protocol.PacketHeader, protocol.Serializable, error) {
	head := make([]byte, protocol.PacketHeaderSize)
	if _, err := io.ReadFull(f.conn, head); err != nil {
		return protocol.PacketHeader{}, nil, err
	}
	var header protocol.PacketHeader
	if err := header.Get(bytes.NewReader(head)); err != nil {
		return protocol.PacketHeader{}, nil, err
	}
	body := make([]byte, header.SizePacket())
	if _, err := io.ReadFull(f.conn, body); err != nil {
		return protocol.PacketHeader{}, nil, err
	}
	return f.comb.UnpackFrame(append(head, body...))
}

func (f *e2eFrames) write(raw []byte) error {
	_, err := f.conn.Write(raw)
	return err
}

// serveControlledUploader answers the engine exactly like an eMule uploader:
// HelloAnswer, FileAnswer, FileStatusAnswer, AcceptUpload and then SendingPart
// fragments of at most 10240 bytes per requested block.
func serveControlledUploader(ln net.Listener, hash protocol.Hash, content []byte) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	frames := &e2eFrames{conn: conn, comb: clientproto.NewPacketCombiner()}
	pieces := protocol.NewBitField(1)
	pieces.SetBit(0)

	for {
		_, packet, err := frames.read()
		if err != nil {
			return
		}
		switch p := packet.(type) {
		case *clientproto.Hello:
			answer := clientproto.HelloAnswer{
				Hash:  protocol.MustHashFromString("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
				Point: protocol.NewEndpoint(0x043C0570, 4662),
				Properties: protocol.TagList{
					protocol.NewStringTag(0x01, "controlled-uploader"),
					protocol.NewUInt32Tag(0x11, 0x3c),
				},
			}
			raw, err := frames.comb.Pack("client.HelloAnswer", &answer)
			if err != nil {
				return
			}
			if frames.write(raw) != nil {
				return
			}
		case *clientproto.FileRequest:
			raw, err := frames.comb.Pack("client.FileAnswer", &clientproto.FileAnswer{
				Hash: hash,
				Name: protocol.ByteContainer16FromString("controlled.bin"),
			})
			if err != nil {
				return
			}
			if frames.write(raw) != nil {
				return
			}
		case *clientproto.FileStatusRequest:
			raw, err := frames.comb.Pack("client.FileStatusAnswer", &clientproto.FileStatusAnswer{
				Hash:     hash,
				BitField: pieces,
			})
			if err != nil {
				return
			}
			if frames.write(raw) != nil {
				return
			}
		case *clientproto.StartUpload:
			raw, err := frames.comb.Pack("client.AcceptUpload", &clientproto.AcceptUpload{})
			if err != nil {
				return
			}
			if frames.write(raw) != nil {
				return
			}
		case *clientproto.RequestParts32:
			for i := 0; i < p.CurrentFree; i++ {
				begin := int64(p.BeginOffset[i])
				end := int64(p.EndOffset[i])
				for off := begin; off < end; off += maxFragmentSize {
					chunkEnd := off + maxFragmentSize
					if chunkEnd > end {
						chunkEnd = end
					}
					part := clientproto.SendingPart32{
						Hash:        hash,
						BeginOffset: uint32(off),
						EndOffset:   uint32(chunkEnd),
					}
					raw, _, err := frames.comb.PackPayload("client.SendingPart32", &part, content[off:chunkEnd])
					if err != nil {
						return
					}
					if frames.write(raw) != nil {
						return
					}
				}
			}
		}
	}
}

// The whole point of the download engine: a source that offers a file must end
// up with every block queued for the writer. This exercises the real handshake,
// the real part negotiation and the 10240-byte SendingPart fragmentation that
// used to abort the connection with IOException after the first fragment.
func TestEngineDownloadsControlledFile(t *testing.T) {
	const blockSize = 190 * 1024
	const totalSize = 2*blockSize + 4096
	content := make([]byte, totalSize)
	for i := range content {
		content[i] = byte(i*7 + 11)
	}
	hash := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go serveControlledUploader(ln, hash, content)

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

	blocks := (totalSize + blockSize - 1) / blockSize
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		session.PumpIO()
		if len(session.diskTasks) >= blocks {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := len(session.diskTasks); got != blocks {
		t.Fatalf("queued %d of %d blocks for writing (download did not complete)", got, blocks)
	}
	for _, task := range session.diskTasks {
		write, ok := task.callable.(*AsyncWrite)
		if !ok {
			t.Fatalf("unexpected disk task %T", task.callable)
		}
		r := write.Block.Range(totalSize)
		if !bytes.Equal(write.Buffer, content[r.Left:r.Right]) {
			t.Fatalf("block %d payload mismatch (%d bytes)", write.Block.PieceBlock, len(write.Buffer))
		}
	}
}
