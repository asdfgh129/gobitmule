package api

import (
	"context"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

type fakeTaskSourceProvider struct {
	calledHash string
	sources    []core.TaskSource
	err        error
}

func (f *fakeTaskSourceProvider) TaskSources(_ context.Context, hash string) ([]core.TaskSource, error) {
	f.calledHash = hash
	return f.sources, f.err
}

func TestEd2kHashFromInput(t *testing.T) {
	const hash = "7221BFD00304905F4C282A1FBB5E9586"
	cases := []struct {
		raw  string
		want string
	}{
		{"ed2k://|file|name|1234|" + hash + "|/", hash},
		{"ed2k://|file|name|1234|" + hash + "|h=AABB|/", hash},
		{"ED2K://|FILE|name|1234|" + hash + "|/", hash},
		{"ed2k://|file|name|1234|deadbeef|/", ""},
		{"ed2k://|server|1.2.3.4:4661|/", ""},
		{"https://example.com/file", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ed2kHashFromInput(tc.raw); got != tc.want {
			t.Errorf("ed2kHashFromInput(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestEnrichSourcesFromTaskDetail(t *testing.T) {
	const hash = "7221BFD00304905F4C282A1FBB5E9586"
	provider := &fakeTaskSourceProvider{
		sources: []core.TaskSource{{Endpoint: "1.2.3.4:4662", State: "connected"}},
	}
	s := &Server{sources: provider}

	d := &core.TaskDetail{}
	d.Input = "ed2k://|file|name|1234|" + hash + "|/"
	s.enrichSources(context.Background(), d)
	if provider.calledHash != hash {
		t.Fatalf("provider hash = %q, want %q", provider.calledHash, hash)
	}
	if len(d.Sources) != 1 || d.Sources[0].Endpoint != "1.2.3.4:4662" {
		t.Fatalf("task detail sources = %+v, want one peer", d.Sources)
	}

	// The resolved Resource is authoritative when the scheduler attaches it.
	provider.calledHash = ""
	d.Resource = &core.Resource{
		Identity: core.ProtocolIdentity{ED2KFileHash: "00112233445566778899AABBCCDDEEFF"},
	}
	s.enrichSources(context.Background(), d)
	if provider.calledHash != "00112233445566778899AABBCCDDEEFF" {
		t.Fatalf("provider hash = %q, want resource identity hash", provider.calledHash)
	}
}
