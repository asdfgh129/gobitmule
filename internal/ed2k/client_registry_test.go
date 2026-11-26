package goed2k

import "testing"

// TestActiveNetworkSnapshotUsesRegisteredEngine locks in the Web console's
// source of truth: server/KAD status must come from the registered per-task
// download engine, not from the port+1 search engine.
func TestActiveNetworkSnapshotUsesRegisteredEngine(t *testing.T) {
	settings := NewSettings()
	settings.UDPPort = 54672
	client := NewClient(settings)
	client.EnableDHT()
	RegisterActiveClient(client)
	defer UnregisterActiveClient(client)

	snapshot := ActiveNetworkStatus()
	if !snapshot.HasEngine {
		t.Fatal("registered download engine was not reported")
	}
	if snapshot.KadStatus == nil {
		t.Fatal("registered download engine KAD status missing")
	}
	if snapshot.KadStatus.ListenPort != settings.UDPPort {
		t.Fatalf("KAD listen port = %d, want %d", snapshot.KadStatus.ListenPort, settings.UDPPort)
	}
	if snapshot.KadState == nil {
		t.Fatal("registered download engine KAD state missing")
	}
}
