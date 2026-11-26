package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/api"
	goed2k "github.com/asdfgh129/gobitmule/internal/ed2k"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

func TestServerMetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.met")
	met := serverproto.NewServerMet()
	entry, err := serverproto.NewServerMetEntryFromHost("example.org", 4661, "Example", "test server")
	if err != nil {
		t.Fatal(err)
	}
	met.AddServer(entry)
	if err := writeServerMet(path, &met); err != nil {
		t.Fatal(err)
	}
	entries, err := metEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "Example" || entries[0].Address() != "example.org:4661" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestServerMetRejectsRemoteWrite(t *testing.T) {
	met := serverproto.NewServerMet()
	if err := writeServerMet("https://example.org/server.met", &met); err == nil ||
		!strings.Contains(err.Error(), "本地文件") {
		t.Fatalf("remote write error = %v", err)
	}
}

// TestEnrichServerRowsWritesBackClientID locks in the HighID indicator: live
// connection state (connected/handshake/clientId) must be reflected onto the
// matching server.met row that is returned to the API, not onto a copy.
func TestEnrichServerRowsWritesBackClientID(t *testing.T) {
	rows := []api.ED2KServerEntry{
		{Address: "176.123.5.89:4725", Name: "eMule Sunrise", Source: "server.met"},
		{Address: "213.252.245.239:43333", Name: "Astra-3", Source: "server.met"},
	}
	runtime := []goed2k.ServerConnectionInfo{
		{
			Identifier:        "176.123.5.89:4725",
			Address:           "176.123.5.89:4725",
			Connected:         true,
			HandshakeComplete: true,
			ClientID:          71042416, // 0x043C0570 -> HighID
		},
	}
	got := enrichServerRows(rows, runtime)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if !got[0].Connected || !got[0].HandshakeComplete || got[0].ClientID != 71042416 {
		t.Fatalf("matching row was not enriched: %+v", got[0])
	}
	if got[1].Connected || got[1].ClientID != 0 {
		t.Fatalf("non-matching row was mutated: %+v", got[1])
	}
}

// TestEnrichServerRowsAddsRuntimeOnly covers runtime servers absent from
// server.met: they must still surface as their own row.
func TestEnrichServerRowsAddsRuntimeOnly(t *testing.T) {
	rows := []api.ED2KServerEntry{{Address: "a:1", Source: "server.met"}}
	runtime := []goed2k.ServerConnectionInfo{
		{Identifier: "b:2", Address: "b:2", Connected: true, ClientID: 123},
	}
	got := enrichServerRows(rows, runtime)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[1].Address != "b:2" || got[1].Source != "运行时" || !got[1].Connected || got[1].ClientID != 123 {
		t.Fatalf("runtime-only row wrong: %+v", got[1])
	}
}

func TestNormalizeServerHostPort(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
	}{
		{"127.0.0.1:4661", false},
		{"example.org:4661", false},
		{"example.org", true},
		{"example.org:0", true},
		{"example.org:70000", true},
	} {
		_, _, err := normalizeServerHostPort(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("normalize(%q) error=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}
