package resolve

import (
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

const (
	testMD4  = "d41d8cd98f00b204e9800998ecf8427e"
	testAICH = "000102030405060708090a0b0c0d0e0f10111213"
)

func TestParseED2KLinkCanonical(t *testing.T) {
	name, size, hash, aich, err := ParseED2KLink("ed2k://|file|Ubuntu-22.04.iso|3221225472|" + testMD4 + "|/")
	if err != nil {
		t.Fatalf("canonical link rejected: %v", err)
	}
	if name != "Ubuntu-22.04.iso" || size != 3221225472 || hash != testMD4 || aich != "" {
		t.Fatalf("bad parse: name=%q size=%d hash=%q aich=%q", name, size, hash, aich)
	}
}

func TestParseED2KLinkWithAICH(t *testing.T) {
	_, _, hash, aich, err := ParseED2KLink("ed2k://|file|movie.mkv|1048576|" + testMD4 + "|h=" + testAICH + "|/")
	if err != nil {
		t.Fatalf("AICH link rejected: %v", err)
	}
	if hash != testMD4 || aich != testAICH {
		t.Fatalf("bad AICH parse: hash=%q aich=%q", hash, aich)
	}
}

func TestParseED2KLinkWithSources(t *testing.T) {
	name, size, hash, _, err := ParseED2KLink(
		"ed2k://|file|a.bin|512|" + testMD4 + "|/sources/192.168.1.2:4662/|/")
	if err != nil {
		t.Fatalf("sources link rejected: %v", err)
	}
	if name != "a.bin" || size != 512 || hash != testMD4 {
		t.Fatalf("bad parse with sources: %q %d %q", name, size, hash)
	}
}

func TestParseED2KLinkRejects(t *testing.T) {
	cases := []string{
		"",
		"https://example.com/a.bin",
		"ed2k://",
		"ed2k://|file|name",
		"ed2k://|server|1.2.3.4:4662|/",
		"ed2k://|file|name|notanumber|" + testMD4 + "|/",
		"ed2k://|file|name|0|" + testMD4 + "|/",
		"ed2k://|file|name|100|deadbeef|/",                 // short hash
		"ed2k://|file|name|100|" + strings.Repeat("z", 32) + "|/", // non-hex
		"ed2k://|file|name|100|" + testMD4 + "|h=short|/",  // bad AICH
		"ed2k://|file|name\r\nx|100|" + testMD4 + "|/",     // control chars
	}
	for _, raw := range cases {
		if _, _, _, _, err := ParseED2KLink(raw); err == nil {
			t.Errorf("link should be rejected: %q", raw)
		}
	}
}

func TestED2KResolverMatch(t *testing.T) {
	r := NewED2KResolver()
	if r.Name() != "ed2k" {
		t.Fatalf("name=%q", r.Name())
	}
	if !r.Match(core.Input{Kind: core.InputED2K, Raw: "ed2k://|file|x|1|" + testMD4 + "|/"}) {
		t.Fatal("InputED2K must match")
	}
	if !r.Match(core.Input{Raw: "ED2K://|file|x|1|" + testMD4 + "|/"}) {
		t.Fatal("case-insensitive ed2k:// prefix must match")
	}
	if r.Match(core.Input{Kind: core.InputURL, Raw: "https://example.com/x"}) {
		t.Fatal("http URL must not match ed2k resolver")
	}
}

func TestED2KResolverResolve(t *testing.T) {
	r := NewED2KResolver()
	raw := "ed2k://|file|big.iso|9999|" + testMD4 + "|h=" + testAICH + "|/"
	res, err := r.Resolve(core.ResolveContext{}, core.Input{Kind: core.InputED2K, Raw: raw})
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.Kind != core.KindED2K || res.Engine != core.EngineGo {
		t.Fatalf("bad resource kind/engine: %s/%s", res.Kind, res.Engine)
	}
	if len(res.Files) != 1 || res.Files[0].Path != "big.iso" || res.Files[0].Size != 9999 || !res.Files[0].Selected {
		t.Fatalf("bad files: %+v", res.Files)
	}
	if res.Identity.ED2KFileHash != testMD4 || res.Identity.AICHRoot != testAICH {
		t.Fatalf("bad identity: %+v", res.Identity)
	}
	if len(res.Mirrors) != 0 {
		t.Fatalf("eD2k resource must not carry HTTP mirrors: %+v", res.Mirrors)
	}
	if !strings.HasPrefix(res.ID, "res-") {
		t.Fatalf("bad resource id %q", res.ID)
	}
}

func TestED2KResolverResolveRejectsMalformed(t *testing.T) {
	r := NewED2KResolver()
	_, err := r.Resolve(core.ResolveContext{}, core.Input{Kind: core.InputED2K, Raw: "ed2k://garbage"})
	if err == nil {
		t.Fatal("malformed link must be rejected")
	}
}
