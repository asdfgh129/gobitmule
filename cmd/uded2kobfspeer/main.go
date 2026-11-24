// Command uded2kobfspeer is the WBS 16 / ED-05 lab interop peer. It speaks
// ONLY the eMule basic-obfuscation stream (no eD2k application bytes) over a
// real TCP socket, so two instances prove cross-process, obfuscation-only
// interoperability with the fork's wire codec on real hardware.
//
// Roles:
//
//	-listen : WrapIncoming (client B) on an accepted socket
//	-dial   : WrapOutgoing (client A) on a connected socket
//
// Both sides must share the same user hash (the key anchor). Each side writes
// a nonce payload and reads the peer's; success means the A/B handshake,
// directional RC4 keys and encrypted transport all interoperate.
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol/obfuscation"
)

type report struct {
	Role        string `json:"role"`
	Addr        string `json:"addr"`
	Peer        string `json:"peer"`
	UserHash    string `json:"user_hash"`
	Obfuscated  bool   `json:"obfuscated"`
	SentNonce   string `json:"sent_nonce"`
	RecvNonce   string `json:"recv_nonce"`
	NoncesMatch bool   `json:"nonces_match"`
	Verdict     string `json:"verdict"`
	BlockReason string `json:"block_reason,omitempty"`
}

func printJSON(r report) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
}

func exchange(conn net.Conn, sentNonce string) (string, error) {
	// Frame: 2-byte big-endian length + payload, carried inside the encrypted
	// stream. Nonce is printed so two independent processes can be compared.
	payload := []byte(sentNonce)
	frame := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(payload)))
	copy(frame[2:], payload)
	if _, err := conn.Write(frame); err != nil {
		return "", fmt.Errorf("write nonce: %w", err)
	}
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", fmt.Errorf("read nonce length: %w", err)
	}
	n := int(binary.BigEndian.Uint16(head))
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", fmt.Errorf("read nonce payload: %w", err)
	}
	return string(buf), nil
}

func main() {
	listen := flag.Bool("listen", false, "act as incoming obfuscation peer (client B)")
	addr := flag.String("addr", "127.0.0.1:47321", "listen address (-listen) or dial target (-dial)")
	hashStr := flag.String("hash", "22222222222222222222222222222222", "shared 32-hex user hash (key anchor)")
	nonce := flag.String("nonce", "ed05-obfs-interop", "nonce payload to exchange")
	timeout := flag.Int("timeout", 15, "handshake/exchange timeout seconds")
	flag.Parse()

	userHash, err := protocol.HashFromString(*hashStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -hash: %v\n", err)
		os.Exit(2)
	}

	r := report{Role: "dial", Addr: *addr, UserHash: userHash.String(), SentNonce: *nonce}
	if *listen {
		r.Role = "listen"
	}

	var stream net.Conn
	obfuscated := false

	if *listen {
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			r.Verdict, r.BlockReason = "FAIL", "listen: "+err.Error()
			printJSON(r)
			os.Exit(2)
		}
		defer ln.Close()
		_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(time.Duration(*timeout) * time.Second))
		raw, err := ln.Accept()
		if err != nil {
			r.Verdict, r.BlockReason = "FAIL", "accept: "+err.Error()
			printJSON(r)
			os.Exit(2)
		}
		r.Peer = raw.RemoteAddr().String()
		stream, obfuscated, err = obfuscation.WrapIncoming(raw, userHash, []byte{0xAA, 0xBB})
		if err != nil {
			r.Verdict, r.BlockReason = "FAIL", "WrapIncoming: "+err.Error()
			_ = raw.Close()
			printJSON(r)
			os.Exit(2)
		}
	} else {
		raw, err := net.DialTimeout("tcp", *addr, time.Duration(*timeout)*time.Second)
		if err != nil {
			r.Verdict, r.BlockReason = "FAIL", "dial: "+err.Error()
			printJSON(r)
			os.Exit(2)
		}
		r.Peer = raw.LocalAddr().String()
		stream, err = obfuscation.WrapOutgoing(raw, userHash, []byte{0x01, 0x02, 0x03})
		if err != nil {
			r.Verdict, r.BlockReason = "FAIL", "WrapOutgoing: "+err.Error()
			_ = raw.Close()
			printJSON(r)
			os.Exit(2)
		}
		obfuscated = true
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(time.Duration(*timeout) * time.Second))

	r.Obfuscated = obfuscated
	if !obfuscated {
		r.Verdict = "FAIL"
		r.BlockReason = "peer did not negotiate obfuscation (plaintext fallback)"
		printJSON(r)
		os.Exit(2)
	}

	recv, err := exchange(stream, *nonce)
	if err != nil {
		r.Verdict, r.BlockReason = "FAIL", err.Error()
		printJSON(r)
		os.Exit(2)
	}
	r.RecvNonce = recv
	r.NoncesMatch = recv == *nonce
	if !r.NoncesMatch {
		r.Verdict = "FAIL"
		r.BlockReason = "nonce mismatch across obfuscated stream"
		printJSON(r)
		os.Exit(2)
	}
	r.Verdict = "PASS"
	printJSON(r)
}
