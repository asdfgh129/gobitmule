// Command uded2khelloprobe is an evidence-collection-only third-party
// interop probe for ED-01. It dials a real eD2k peer (e.g. eMule on a lab host)
// over a real TCP socket and performs the eD2k Hello / HelloAnswer exchange
// using the fork's exact client wire codec. Success means BOTH sides see each
// other at the connection layer: we learn the peer's 16-byte user hash from
// its HelloAnswer, and the peer accepted our Hello carrying our user hash.
//
// It never touches ud state or the daemon. Run it on a lab host.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol/obfuscation"
)

type report struct {
	Timestamp       string   `json:"timestamp"`
	Peer            string   `json:"peer"`
	LocalUserHash   string   `json:"local_user_hash"`
	RemoteHash      string   `json:"remote_user_hash,omitempty"`
	RemotePoint     string   `json:"remote_point,omitempty"`
	ModName         string   `json:"remote_mod,omitempty"`
	Verdict         string   `json:"verdict"`
	BlockReason     string   `json:"block_reason,omitempty"`
	FileRequestSent bool     `json:"file_request_sent,omitempty"`
	PostReqPackets  []string `json:"post_req_packets,omitempty"`
	PostReqError    string   `json:"post_req_error,omitempty"`
	Obfuscation     string   `json:"obfuscation,omitempty"`
	ObfError        string   `json:"obfuscation_error,omitempty"`
}

func readFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, protocol.PacketHeaderSize)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	var parsed protocol.PacketHeader
	if err := parsed.Get(bytes.NewReader(header)); err != nil {
		return nil, err
	}
	bodyLen := int(parsed.SizePacket())
	if bodyLen < 0 || bodyLen > 1<<20 {
		return nil, io.ErrUnexpectedEOF
	}
	frame := make([]byte, protocol.PacketHeaderSize+bodyLen)
	copy(frame, header)
	if _, err := io.ReadFull(conn, frame[protocol.PacketHeaderSize:]); err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	return frame, nil
}

// readFramePoll mimics the engine's DoRead exactly: a short (50ms) read
// deadline into a fixed 16KB buffer, bytes accumulated across ticks, then
// parsed as full frames. Timeout is not an error; it returns control to the
// caller to retry on the next tick. This isolates the IO strategy as a
// variable in the handshake comparison.
func readFramePoll(conn net.Conn, incoming *[]byte, poll time.Duration, overall time.Duration) ([]byte, error) {
	deadline := time.Now().Add(overall)
	tmp := make([]byte, 16*1024)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(poll))
		n, err := conn.Read(tmp)
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n > 0 {
			*incoming = append(*incoming, tmp[:n]...)
			if len(*incoming) >= protocol.PacketHeaderSize {
				var header protocol.PacketHeader
				r := bytes.NewReader(*incoming)
				if err := header.Get(r); err != nil {
					return nil, err
				}
				bodyLen := int(header.SizePacket())
				if bodyLen < 0 || bodyLen > 1<<20 {
					return nil, io.ErrUnexpectedEOF
				}
				need := protocol.PacketHeaderSize + bodyLen
				if len(*incoming) >= need {
					frame := append([]byte(nil), (*incoming)[:need]...)
					*incoming = (*incoming)[need:]
					return frame, nil
				}
			}
		}
	}
	return nil, os.ErrDeadlineExceeded
}

func main() {
	os.Exit(run())
}

func run() int {
	peer := flag.String("peer", "192.168.1.209:31969", "external eD2k peer (eMule) address")
	userHash := flag.String("user-hash", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "local user hash to announce (32 hex)")
	listenPort := flag.Int("listen-port", 47941, "TCP port we advertise in our Hello")
	timeout := flag.Int("timeout", 10, "seconds to wait for HelloAnswer")
	fileHash := flag.String("file-hash", "", "if set, send FileRequest right after HelloAnswer (handshake-order probe)")
	rawHelloHex := flag.String("raw-hello", "", "if set, send this exact hex Hello frame instead of building one")
	obfPeerHash := flag.String("obf-peer-hash", "", "if set, wrap the socket with eMule basic obfuscation using this peer hash (32 hex)")
	pollRead := flag.Bool("poll-read", false, "mimic engine DoRead: 50ms deadline + 16KB buffer + accumulated frames")
	flag.Parse()

	r := report{Timestamp: time.Now().UTC().Format(time.RFC3339), Peer: *peer, LocalUserHash: *userHash, Verdict: "NOT_EXECUTED"}
	localHash, err := protocol.HashFromString(*userHash)
	if err != nil {
		r.BlockReason = "invalid -user-hash: " + err.Error()
		emit(r)
		return 2
	}

	conn, err := net.DialTimeout("tcp", *peer, time.Duration(*timeout)*time.Second)
	if err != nil {
		r.BlockReason = "dial peer: " + err.Error()
		emit(r)
		return 2
	}
	defer conn.Close()
	var ioConn net.Conn = conn
	if *obfPeerHash != "" {
		peerHash, herr := protocol.HashFromString(*obfPeerHash)
		if herr != nil {
			r.BlockReason = "invalid -obf-peer-hash: " + herr.Error()
			emit(r)
			return 2
		}
		wrapped, werr := obfuscation.WrapOutgoing(conn, peerHash, nil)
		if werr != nil {
			r.Obfuscation = "failed"
			r.ObfError = werr.Error()
			r.Verdict = "OBF_HANDSHAKE_FAILED"
			emit(r)
			return 2
		}
		ioConn = wrapped
		r.Obfuscation = "enabled"
	}
	_ = ioConn.SetDeadline(time.Now().Add(time.Duration(*timeout) * time.Second))

	combiner := clientproto.NewPacketCombiner()
	hello := clientproto.Hello{
		HashLength: 16,
		HelloAnswer: clientproto.HelloAnswer{
			Hash:  localHash,
			Point: protocol.NewEndpoint(0x70000001, *listenPort), // High ID + our port
			Properties: protocol.TagList{
				protocol.NewStringTag(0x01, "ud-fork-probe"),
				protocol.NewStringTag(0x55, "udFork"),
				protocol.NewUInt32Tag(0x11, 0x3c),
				protocol.NewUInt32Tag(0xFB, 0x03000000),
			},
			ServerPoint: protocol.Endpoint{},
		},
	}
	var raw []byte
	if *rawHelloHex != "" {
		raw, err = hex.DecodeString(*rawHelloHex)
		if err != nil {
			r.BlockReason = "invalid -raw-hello hex: " + err.Error()
			emit(r)
			return 2
		}
	} else {
		raw, err = combiner.Pack("client.Hello", &hello)
		if err != nil {
			r.BlockReason = "pack Hello: " + err.Error()
			emit(r)
			return 2
		}
	}
	if _, err := ioConn.Write(raw); err != nil {
		r.BlockReason = "write Hello: " + err.Error()
		emit(r)
		return 2
	}

	// Read frames until we get a HelloAnswer (eMule may send ExtHello first).
	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	incoming := make([]byte, 0, 16*1024)
	for time.Now().Before(deadline) {
		var frame []byte
		var err error
		if *pollRead {
			frame, err = readFramePoll(ioConn, &incoming, 50*time.Millisecond, time.Until(deadline))
		} else {
			frame, err = readFrame(ioConn)
		}
		if err != nil {
			r.BlockReason = "read frame: " + err.Error()
			emit(r)
			return 2
		}
		_, packet, err := combiner.UnpackFrame(frame)
		if err != nil {
			r.BlockReason = "unpack frame: " + err.Error()
			emit(r)
			return 2
		}
		switch v := packet.(type) {
		case *clientproto.HelloAnswer:
			r.RemoteHash = v.Hash.String()
			r.RemotePoint = fmt.Sprintf("%d", v.Point.Port())
			for _, tag := range v.Properties {
				if tag.ID == 0x55 && tag.String != "" {
					r.ModName = tag.String
				}
			}
			r.Verdict = "PASS"
			if *fileHash == "" {
				emit(r)
				return 0
			}
			// Handshake-order probe: immediately request a file after the
			// HelloAnswer (mirrors PeerConnection.HandleHelloAnswer). Observe
			// whether the peer keeps the connection open or drops it.
			fh, ferr := protocol.HashFromString(*fileHash)
			if ferr != nil {
				r.BlockReason = "invalid -file-hash: " + ferr.Error()
				emit(r)
				return 2
			}
			reqRaw, perr := combiner.Pack("client.FileRequest", &clientproto.FileRequest{Hash: fh})
			if perr != nil {
				r.BlockReason = "pack FileRequest: " + perr.Error()
				emit(r)
				return 2
			}
			if _, werr := ioConn.Write(reqRaw); werr != nil {
				r.PostReqError = "write FileRequest: " + werr.Error()
				emit(r)
				return 0
			}
			r.FileRequestSent = true
			postDeadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(postDeadline) {
				postFrame, rerr := readFrame(ioConn)
				if rerr != nil {
					r.PostReqError = "read after FileRequest: " + rerr.Error()
					break
				}
				_, pp, uerr := combiner.UnpackFrame(postFrame)
				if uerr != nil {
					r.PostReqPackets = append(r.PostReqPackets, "unpack-error:"+uerr.Error())
					continue
				}
				r.PostReqPackets = append(r.PostReqPackets, fmt.Sprintf("%T", pp))
			}
			emit(r)
			return 0
		case *clientproto.ExtHello:
			// Reply to eMule's extended handshake with a minimal answer.
			ans := clientproto.ExtHelloAnswer{ExtendedHandshake: clientproto.ExtendedHandshake{}}
			if raw, err := combiner.Pack("client.ExtHelloAnswer", &ans); err == nil {
				_, _ = ioConn.Write(raw)
			}
		default:
			// ignore other packets while waiting for HelloAnswer
		}
	}
	r.BlockReason = "no HelloAnswer within timeout"
	emit(r)
	return 2
}

func emit(r report) {
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
}
