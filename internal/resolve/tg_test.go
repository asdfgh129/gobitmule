package resolve

// Offline tests for the Telegram resolver (WBS 15, ADR-0029).

import (
	"strings"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
	tgtransport "github.com/asdfgh129/ud/internal/transport/tg"
)

func TestTGResolverMatch(t *testing.T) {
	r := NewTGResolver()
	yes := []string{
		"https://t.me/durov/42",
		"https://telegram.me/durov/42",
		"tg://resolve?domain=durov&post=42",
	}
	for _, raw := range yes {
		if !r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = false, want true", raw)
		}
	}
	no := []string{
		"https://t.me/durov",      // no message id
		"https://example.com/x/1", // wrong host
		"magnet:?xt=urn:btih:abc", // not a URL kind issue — magnet kind
		"tg://stickers?set=durov", // unsupported tg deep link
	}
	for _, raw := range no {
		if r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = true, want false", raw)
		}
	}
	if r.Match(core.Input{Raw: "https://t.me/durov/42", Kind: core.InputMagnet}) {
		t.Error("non-URL kinds must not match")
	}
}

func TestTGResolveNormalizesAndCarriesCredential(t *testing.T) {
	r := NewTGResolver()
	res, err := r.Resolve(core.ResolveContext{
		Credential: "keychain://ud/cred/abc",
	}, core.Input{Raw: "https://t.me/durov/42", Kind: core.InputURL})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Kind != core.KindSingle {
		t.Fatalf("kind = %s", res.Kind)
	}
	mirror := res.Mirrors[0].URL
	if !strings.HasPrefix(mirror, "tg://ud/link?") {
		t.Fatalf("mirror = %q, want the internal tg://ud form", mirror)
	}
	if res.Identity.VariantID != "tg:durov/42" {
		t.Fatalf("variant = %q", res.Identity.VariantID)
	}
	// The MTProto session travels as a credential reference (FR-SRC-06).
	if res.Credential != "keychain://ud/cred/abc" {
		t.Fatalf("credential = %q", res.Credential)
	}
	// Dispatch: no http(s) transport may claim the tg:// mirror.
	back, err := tgtransport.ParseNormalized(mirror)
	if err != nil || back.Channel != "durov" || back.MsgID != 42 {
		t.Fatalf("mirror not re-parseable: %v / %+v", err, back)
	}
}

func TestTGResolverRejectsBadLinks(t *testing.T) {
	r := NewTGResolver()
	for _, raw := range []string{
		"https://t.me/durov",
		"https://t.me/s/durov",
		"tg://stickers?set=durov",
	} {
		if _, err := r.Resolve(core.ResolveContext{}, core.Input{Raw: raw, Kind: core.InputURL}); err == nil {
			t.Errorf("Resolve(%q) must fail", raw)
		}
	}
}
