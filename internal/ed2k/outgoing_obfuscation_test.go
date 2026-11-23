package goed2k

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/asdfgh129/ud/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/ud/internal/ed2k/protocol/kad"
	"github.com/asdfgh129/ud/internal/ed2k/protocol/obfuscation"
)

// TestPeerConnectionAutoArmsOutgoingObfuscation drives the production source
// path end to end: a Peer carrying a target hash goes through
// PeerConnection.Connect (the same call Transfer.ConnectToPeer uses) against a
// real TCP listener that accepts obfuscation only. The encrypted payload
// round-trip proves the hash reached the A-side handshake, not just a field.
func TestPeerConnectionAutoArmsOutgoingObfuscation(t *testing.T) {
	targetHash := protocol.MustHashFromString("22222222222222222222222222222222")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer raw.Close()
		stream, obf, err := obfuscation.WrapIncoming(raw, targetHash, nil)
		if err != nil {
			serverErr <- fmt.Errorf("incoming handshake: %w", err)
			return
		}
		if !obf {
			serverErr <- fmt.Errorf("peer connected in plaintext to an obfuscation-only listener")
			return
		}
		buf := make([]byte, 5)
		if _, err := io.ReadFull(stream, buf); err != nil {
			serverErr <- err
			return
		}
		if string(buf) != "hello" {
			serverErr <- fmt.Errorf("payload mismatch: %q", buf)
			return
		}
		_, err = stream.Write([]byte("world"))
		serverErr <- err
	}()

	session := &Session{}
	st := NewSettings()
	st.EnableObfuscation = true
	session.ConfigureSession(st)

	endpoint := protocol.EndpointFromInet(ln.Addr().(*net.TCPAddr))
	peer := NewPeerWithSourceAndHash(endpoint, true, int(PeerDHT), targetHash)
	pc := NewPeerConnection(session, endpoint, nil, &peer)
	if err := pc.Connect(); err != nil {
		t.Fatalf("PeerConnection.Connect: %v", err)
	}
	defer func() { _ = pc.socket.Close() }()

	if !pc.IsObfuscated() {
		t.Fatal("outgoing connection stayed plaintext despite a known target hash")
	}
	if !pc.obfPeerHash.Equal(targetHash) {
		t.Fatalf("outgoing peer hash was not armed: got %s want %s", pc.obfPeerHash.String(), targetHash.String())
	}
	if _, err := pc.socket.Write([]byte("hello")); err != nil {
		t.Fatalf("write encrypted payload: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(pc.socket, buf); err != nil {
		t.Fatalf("read encrypted reply: %v", err)
	}
	if string(buf) != "world" {
		t.Fatalf("encrypted reply mismatch: %q", buf)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("obfuscation-only server: %v", err)
	}
}

// TestPeerConnectionStaysPlaintextWithoutHash guards compatibility: an
// unknown target hash must never trigger WrapOutgoing, otherwise normal eD2k
// peers would receive a marker instead of their expected packet header.
func TestPeerConnectionStaysPlaintextWithoutHash(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer raw.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(raw, buf); err != nil {
			serverErr <- err
			return
		}
		if string(buf) != "hello" {
			serverErr <- fmt.Errorf("plaintext payload mismatch: %q", buf)
			return
		}
		_, err = raw.Write([]byte("world"))
		serverErr <- err
	}()

	session := &Session{}
	st := NewSettings()
	st.EnableObfuscation = true
	session.ConfigureSession(st)

	endpoint := protocol.EndpointFromInet(ln.Addr().(*net.TCPAddr))
	peer := NewPeerWithSource(endpoint, true, int(PeerDHT))
	pc := NewPeerConnection(session, endpoint, nil, &peer)
	if err := pc.Connect(); err != nil {
		t.Fatalf("PeerConnection.Connect: %v", err)
	}
	defer func() { _ = pc.socket.Close() }()

	if pc.IsObfuscated() {
		t.Fatal("connection was obfuscated without a known target hash")
	}
	if _, err := pc.socket.Write([]byte("hello")); err != nil {
		t.Fatalf("write plaintext payload: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(pc.socket, buf); err != nil {
		t.Fatalf("read plaintext reply: %v", err)
	}
	if string(buf) != "world" {
		t.Fatalf("plaintext reply mismatch: %q", buf)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("plaintext server: %v", err)
	}
}

// TestPeerConnectionObeysObfuscationOff verifies the configuration switch is
// authoritative even when a hash is available.
func TestPeerConnectionObeysObfuscationOff(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer raw.Close()
		buf := make([]byte, 4)
		_, err = io.ReadFull(raw, buf)
		serverErr <- err
	}()

	targetHash := protocol.MustHashFromString("33333333333333333333333333333333")
	session := &Session{}
	st := NewSettings() // EnableObfuscation is false by default
	session.ConfigureSession(st)

	endpoint := protocol.EndpointFromInet(ln.Addr().(*net.TCPAddr))
	peer := NewPeerWithSourceAndHash(endpoint, true, int(PeerDHT), targetHash)
	pc := NewPeerConnection(session, endpoint, nil, &peer)
	if err := pc.Connect(); err != nil {
		t.Fatalf("PeerConnection.Connect: %v", err)
	}
	defer func() { _ = pc.socket.Close() }()
	if pc.IsObfuscated() {
		t.Fatal("obfuscation was armed while EnableObfuscation=false")
	}
	if _, err := pc.socket.Write([]byte("ping")); err != nil {
		t.Fatalf("write plaintext probe: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("plaintext server: %v", err)
	}
}

// TestPolicyAddPeerMergesLearnedHash covers the dual-source case: server or
// resume data first inserts a hash-less contact, KAD/Hello later supplies the
// target hash. The stored contact must keep it for the next connect.
func TestPolicyAddPeerMergesLearnedHash(t *testing.T) {
	var policy Policy
	endpoint := protocol.NewEndpoint(0x0100007F, 4662)
	learned := protocol.MustHashFromString("44444444444444444444444444444444")

	added, err := policy.AddPeer(NewPeerWithSource(endpoint, true, int(PeerServer)))
	if err != nil || !added {
		t.Fatalf("first AddPeer failed: added=%v err=%v", added, err)
	}
	added, err = policy.AddPeer(NewPeerWithSourceAndHash(endpoint, true, int(PeerDHT), learned))
	if err != nil || added {
		t.Fatalf("second AddPeer should merge: added=%v err=%v", added, err)
	}
	stored := policy.Get(endpoint)
	if stored == nil {
		t.Fatal("merged peer not found")
	}
	if !stored.UserHash.Equal(learned) {
		t.Fatalf("learned hash was dropped: got %s want %s", stored.UserHash.String(), learned.String())
	}
	if stored.SourceFlag&int(PeerServer) == 0 || stored.SourceFlag&int(PeerDHT) == 0 {
		t.Fatalf("source flags were not merged: %d", stored.SourceFlag)
	}
}

// TestLearnedHelloHashArmsReconnect checks the fallback data path: a cleartext
// Hello reveals the real user hash, which is written back to the shared Peer
// so a later connection can negotiate obfuscation without KAD metadata.
func TestLearnedHelloHashArmsReconnect(t *testing.T) {
	learned := protocol.MustHashFromString("55555555555555555555555555555555")
	session := &Session{}
	st := NewSettings()
	st.EnableObfuscation = true
	session.ConfigureSession(st)

	endpoint := protocol.NewEndpoint(0x0100007F, 4662)
	peer := NewPeerWithSource(endpoint, true, int(PeerServer))
	pc := NewPeerConnection(session, endpoint, nil, &peer)

	pc.learnRemoteHash(learned)
	if peer.UserHash.Equal(protocol.Invalid) {
		t.Fatal("Hello hash was not written back to the shared Peer")
	}
	if !peer.UserHash.Equal(learned) {
		t.Fatalf("learned hash mismatch: got %s want %s", peer.UserHash.String(), learned.String())
	}
	if !pc.obfPeerHash.Equal(protocol.Invalid) {
		t.Fatal("learnRemoteHash must not arm the current (cleartext) connection")
	}
}

// TestTentativeObfuscationFallsBackToPlaintext guards the KAD-ID assumption:
// if a source-derived hash is wrong and the peer rejects the handshake, a
// fresh cleartext socket must be used instead of dropping the source.
func TestTentativeObfuscationFallsBackToPlaintext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		// First connection carries a handshake with the wrong key anchor.
		bad, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		_ = bad.Close()

		// The retried connection must be plain eD2k traffic.
		raw, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer raw.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(raw, buf); err != nil {
			serverErr <- err
			return
		}
		if string(buf) != "ping" {
			serverErr <- fmt.Errorf("fallback payload mismatch: %q", buf)
			return
		}
		_, err = raw.Write([]byte("pong"))
		serverErr <- err
	}()

	session := &Session{}
	st := NewSettings()
	st.EnableObfuscation = true
	session.ConfigureSession(st)

	endpoint := protocol.EndpointFromInet(ln.Addr().(*net.TCPAddr))
	wrongHash := protocol.MustHashFromString("66666666666666666666666666666666")
	peer := NewPeerWithSourceAndHash(endpoint, true, int(PeerDHT), wrongHash)
	pc := NewPeerConnection(session, endpoint, nil, &peer)
	if err := pc.Connect(); err != nil {
		t.Fatalf("PeerConnection.Connect did not fall back: %v", err)
	}
	defer func() { _ = pc.socket.Close() }()
	if pc.IsObfuscated() {
		t.Fatal("connection should have fallen back to plaintext")
	}
	if _, err := pc.socket.Write([]byte("ping")); err != nil {
		t.Fatalf("write fallback payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(pc.socket, buf); err != nil {
		t.Fatalf("read fallback reply: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("fallback reply mismatch: %q", buf)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("fallback server: %v", err)
	}
}

// TestKADSourceHashReachesPeer drives Session.SendDHTSourcesRequest against
// the in-process controlled KAD peer and verifies the 16-byte source ID is
// stored on the source candidate, so the normal download path can arm
// outgoing obfuscation without a second discovery round.
func TestKADSourceHashReachesPeer(t *testing.T) {
	peer := newFakeKADPeer(t)
	defer peer.close()

	tracker := NewDHTTracker(0, 900*time.Millisecond)
	tracker.rpc.shortTimeout = 200 * time.Millisecond
	tracker.rpc.timeout = 900 * time.Millisecond
	if err := tracker.Start(); err != nil {
		t.Fatalf("tracker Start: %v", err)
	}
	defer tracker.Close()

	tracker.mu.Lock()
	node := tracker.addOrUpdateNodeLocked(peer.selfID, peer.addr(), 4662, kadproto.KademliaVersion, true)
	if node != nil {
		tracker.table.NodeSeen(node)
	}
	tracker.mu.Unlock()
	if node == nil {
		t.Fatal("controlled KAD peer was not added to routing table")
	}

	SetDebugLog(&bytes.Buffer{})
	defer SetDebugLog(io.Discard)

	st := NewSettings()
	st.EnableObfuscation = true
	session := NewSession(st)
	session.dhtTracker = tracker

	hash := protocol.MustHashFromString("E75AD307F1EC36B45DD100AB92A2F8C0")
	tmp, err := os.CreateTemp(t.TempDir(), "kad-hash-*.part")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer tmp.Close()
	if _, err := session.AddTransfer(hash, 1038612780, tmp); err != nil {
		t.Fatalf("AddTransfer: %v", err)
	}
	transfer := session.transfers[hash]
	if transfer == nil {
		t.Fatal("transfer was not registered")
	}
	if !session.SendDHTSourcesRequest(hash, 1038612780, transfer) {
		t.Fatal("SendDHTSourcesRequest did not start")
	}

	wantHash := protocol.MustHashFromString("DEADBEEFDEADBEEFDEADBEEFDEADBEEF")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		session.mu.Lock()
		candidate := transfer.policy.Get(protocol.NewEndpoint(0x0100007F, 4662))
		session.mu.Unlock()
		if candidate != nil && candidate.UserHash.Equal(wantHash) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("KAD source ID was not propagated to the source candidate's UserHash")
}
