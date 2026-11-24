package app

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/api"
	goed2k "github.com/asdfgh129/gobitmule/internal/ed2k"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func TestED2KSearchMappings(t *testing.T) {
	if got := toEngineScope(api.ED2KScopeServer); got != goed2k.SearchScopeServer {
		t.Fatalf("server scope = %v", got)
	}
	if got := toEngineScope(api.ED2KScopeKAD); got != goed2k.SearchScopeDHT {
		t.Fatalf("kad scope = %v", got)
	}
	if got := toEngineScope(""); got != goed2k.SearchScopeAll {
		t.Fatalf("default scope = %v", got)
	}
	if got := toAPIScope(goed2k.SearchScopeDHT); got != api.ED2KScopeKAD {
		t.Fatalf("api kad scope = %v", got)
	}
	if got := toResultSource(goed2k.SearchResultServer | goed2k.SearchResultKAD); got != api.ED2KResultBoth {
		t.Fatalf("both source = %v", got)
	}
	if got := toResultSource(goed2k.SearchResultKAD); got != api.ED2KResultKAD {
		t.Fatalf("kad source = %v", got)
	}
}

func TestDedicatedPort(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{4662, 4663}, {65535, 0}, {0, 0}, {-1, 0},
	} {
		if got := dedicatedPort(tc.in); got != tc.want {
			t.Errorf("dedicatedPort(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSnapshotStatusConversion(t *testing.T) {
	hash, err := protocol.HashFromData([]byte("ed2k-search-test"))
	if err != nil {
		t.Fatal(err)
	}
	status := snapshotStatus(goed2k.SearchSnapshot{
		ID:         7,
		State:      goed2k.SearchStateFinished,
		KadKeyword: "ubuntu",
		Results: []goed2k.SearchResult{{
			Hash:            hash,
			FileName:        "ubuntu.iso",
			FileSize:        1024,
			Sources:         3,
			CompleteSources: 2,
			Source:          goed2k.SearchResultServer | goed2k.SearchResultKAD,
		}},
	})
	if !status.Available || status.Snapshot.State != api.ED2KSearchFinished {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Snapshot.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(status.Snapshot.Results))
	}
	row := status.Snapshot.Results[0]
	if row.Source != api.ED2KResultBoth || row.FileName != "ubuntu.iso" || row.Hash != hash.String() {
		t.Fatalf("result = %+v", row)
	}
	if row.ED2KLink == "" {
		t.Fatal("valid result should carry an ed2k link")
	}
}

func TestEmptySearchStatus(t *testing.T) {
	status := emptyStatus()
	if !status.Available || status.Snapshot.State != api.ED2KSearchIdle || status.Snapshot.Results == nil {
		t.Fatalf("empty status = %+v", status)
	}
}
