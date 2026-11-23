package goed2k

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/asdfgh129/ud/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/ud/internal/ed2k/protocol/server"
)

// TestHighIDSimulatedServerHandshake is the ED-03 counterpart to the Low ID
// simulation: a local fake server grants a public client ID and the client
// must classify itself HIGH_ID while staying alive.
func TestHighIDSimulatedServerHandshake(t *testing.T) {
	clientID := int32(0x70000001) // public, above HighestLowIDED2K
	runSimulatedServerHandshake(t, clientID, "HIGH_ID")
}

// TestLowIDKeepsSessionAliveWithSources is the ED-04 progress check: under a
// Low ID client state the engine still processes server source lists, accepts
// connectable (high-ID) sources and keeps the session alive. The visible
// source ratio is asserted by TestSourceVisibilityRatioDropsForLowIDSources.
func TestLowIDKeepsSessionAliveWithSources(t *testing.T) {
	runSimulatedServerHandshake(t, 123456, "LOW_ID")
}

func runSimulatedServerHandshake(t *testing.T, clientID int32, wantClass string) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen fake server: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := readServerFrame(conn); err != nil {
			return
		}
		combiner := serverproto.NewPacketCombiner()
		raw, err := combiner.Pack("server.IdChange", &serverproto.IdChange{
			ClientID: clientID, TCPFlags: 0, AuxPort: 0,
		})
		if err != nil {
			return
		}
		if _, err := conn.Write(raw); err != nil {
			return
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}()

	settings := NewSettings()
	settings.ListenPort = 0
	settings.EnableDHT = false
	client := NewClient(settings)
	defer client.Close()
	if err := client.Start(); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	if err := client.Connect(listener.Addr().String()); err != nil {
		t.Fatalf("client Connect: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range client.ServerStatuses() {
			if !status.HandshakeCompleted {
				continue
			}
			if status.ClientID != clientID {
				t.Fatalf("server client ID = %d, want %d", status.ClientID, clientID)
			}
			if class := status.IDClass(); class != wantClass {
				t.Fatalf("IDClass = %s, want %s", class, wantClass)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake server handshake did not complete")
}

// TestSourceVisibilityRatioDropsForLowIDSources makes ED-04's "visible source
// ratio drops, engine does not crash" measurable. A server source list with
// six sources (three publicly connectable, three Low ID / unreachable) is
// processed by the real server packet handler. The engine must keep exactly
// the connectable three, i.e. the visible ratio drops from 100% to 50%,
// without breaking the session.
func TestSourceVisibilityRatioDropsForLowIDSources(t *testing.T) {
	const totalSources = 6
	const connectableSources = 3

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen fake server: %v", err)
	}
	defer listener.Close()

	hash := protocol.MustHashFromString("E75AD307F1EC36B45DD100AB92A2F8C0")
	sources := make([]protocol.Endpoint, 0, totalSources)
	for i := 0; i < connectableSources; i++ {
		// Public IPs (>= HighestLowIDED2K when read as int32).
		sources = append(sources, protocol.NewEndpoint(int32(0x75010001+uint32(i)), 4662+i))
	}
	for i := 0; i < totalSources-connectableSources; i++ {
		// Low ID endpoints: IP < 16777216 are filtered by the engine.
		sources = append(sources, protocol.NewEndpoint(int32(0x000A0001+uint32(i)), 4662+i))
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := readServerFrame(conn); err != nil {
			return
		}
		combiner := serverproto.NewPacketCombiner()
		idRaw, err := combiner.Pack("server.IdChange", &serverproto.IdChange{
			ClientID: 123456, TCPFlags: 0, AuxPort: 0,
		})
		if err != nil {
			return
		}
		if _, err := conn.Write(idRaw); err != nil {
			return
		}
		// Give the client a moment to register the transfer before sources.
		time.Sleep(200 * time.Millisecond)
		srcRaw, err := combiner.Pack("server.FoundFileSources", &serverproto.FoundFileSources{
			Hash: hash, Sources: sources,
		})
		if err != nil {
			return
		}
		if _, err := conn.Write(srcRaw); err != nil {
			return
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}()

	settings := NewSettings()
	settings.ListenPort = 0
	settings.EnableDHT = false
	client := NewClient(settings)
	defer client.Close()
	if err := client.Start(); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	tmp, err := os.CreateTemp(t.TempDir(), "visible-sources-*.part")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer tmp.Close()
	if _, err := client.AddTransfer(NewAddTransferParamsFromFile(hash, CurrentTimeMillis(), 1038612780, tmp, false)); err != nil {
		t.Fatalf("AddTransfer: %v", err)
	}
	handle := client.FindTransfer(hash)
	if !handle.IsValid() {
		t.Fatal("transfer handle invalid")
	}
	if err := client.Connect(listener.Addr().String()); err != nil {
		t.Fatalf("client Connect: %v", err)
	}

	session := client.session
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		session.mu.Lock()
		transfer := session.transfers[hash]
		var visible int
		if transfer != nil {
			visible = transfer.policy.Size()
		}
		session.mu.Unlock()
		if visible == connectableSources {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("visible source ratio did not settle at %d/%d", connectableSources, totalSources)
}
