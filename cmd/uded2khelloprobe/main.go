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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

type report struct {
	Timestamp     string `json:"timestamp"`
	Peer          string `json:"peer"`
	LocalUserHash string `json:"local_user_hash"`
	RemoteHash    string `json:"remote_user_hash,omitempty"`
	RemotePoint   string `json:"remote_point,omitempty"`
	ModName       string `json:"remote_mod,omitempty"`
	Verdict       string `json:"verdict"`
	BlockReason   string `json:"block_reason,omitempty"`
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
		return nil, err
	}
	return frame, nil
}

func main() {
	os.Exit(run())
}

func run() int {
	peer := flag.String("peer", "192.168.1.209:31969", "external eD2k peer (eMule) address")
	userHash := flag.String("user-hash", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "local user hash to announce (32 hex)")
	listenPort := flag.Int("listen-port", 47941, "TCP port we advertise in our Hello")
	timeout := flag.Int("timeout", 10, "seconds to wait for HelloAnswer")
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
	_ = conn.SetDeadline(time.Now().Add(time.Duration(*timeout) * time.Second))

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
	raw, err := combiner.Pack("client.Hello", &hello)
	if err != nil {
		r.BlockReason = "pack Hello: " + err.Error()
		emit(r)
		return 2
	}
	if _, err := conn.Write(raw); err != nil {
		r.BlockReason = "write Hello: " + err.Error()
		emit(r)
		return 2
	}

	// Read frames until we get a HelloAnswer (eMule may send ExtHello first).
	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	for time.Now().Before(deadline) {
		frame, err := readFrame(conn)
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
			emit(r)
			return 0
		case *clientproto.ExtHello:
			// Reply to eMule's extended handshake with a minimal answer.
			ans := clientproto.ExtHelloAnswer{ExtendedHandshake: clientproto.ExtendedHandshake{}}
			if raw, err := combiner.Pack("client.ExtHelloAnswer", &ans); err == nil {
				_, _ = conn.Write(raw)
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
