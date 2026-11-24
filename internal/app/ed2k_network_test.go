package app

import (
	"path/filepath"
	"strings"
	"testing"

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
