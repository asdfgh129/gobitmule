package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// The packing is what eMule writes for its own 0.70a release; the console should
// print the same label eMule's client list does.
func TestEmuleVersionString(t *testing.T) {
	cases := []struct {
		raw  uint32
		want string
	}{
		{0x00011880, "v0.70a"},  // eMule 0.70a / 0.70b family, observed live
		{0x03040C80, "v2.3a"},   // a mod announcing itself as 2.3
		{0x0A060400, "v3.1"},    // [GrupoTS] mod
		{0x00000000, "v0.0"},    // nothing announced
		{0x00FE1100, ""},        // implausible major/minor: refuse to invent one
	}
	for _, c := range cases {
		if got := emuleVersionString(c.raw); got != c.want {
			t.Errorf("emuleVersionString(0x%x) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// CT_NAME is the project URL on stock clients and the brand on mods. The console
// must show something a human recognises instead of a URL.
func TestClientFamily(t *testing.T) {
	cases := []struct{ modName, ctName, want string }{
		{"", "http://emule-project.net", "eMule"},
		{"", "https://www.emule-project.org", "eMule"},
		{"", "http://www.aMule.org", "aMule"},
		{"", "https://amule-org.github.io", "aMule"},
		{"", "https://www.shareaza.com", "Shareaza"},
		{"", "[GrupoTS]jmiguelfer", "[GrupoTS]jmiguelfer"},
		{"eserver", "eServer", "eserver"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := clientFamily(c.modName, c.ctName); got != c.want {
			t.Errorf("clientFamily(%q, %q) = %q, want %q", c.modName, c.ctName, got, c.want)
		}
	}
}

// End to end over a realistic eMule Hello tag list: version comes from 0xFB, the
// protocol version in 0x11 is not a client version, and the URL becomes "eMule".
func TestApplyClientTagsFromLiveHello(t *testing.T) {
	p := &PeerConnection{}
	p.applyClientTags(protocol.TagList{
		{Type: 2, ID: tagClientName, String: "http://emule-project.net"},
		{Type: 3, ID: tagClientVersion, UInt32: 60},
		{Type: 3, ID: tagEmuleVersion, UInt32: 0x00011880},
	})
	if p.remotePeerInfo.ModName != "eMule" {
		t.Fatalf("client = %q, want eMule", p.remotePeerInfo.ModName)
	}
	if p.remotePeerInfo.ModVersion != "v0.70a" {
		t.Fatalf("version = %q, want v0.70a", p.remotePeerInfo.ModVersion)
	}
	info := p.GetInfo()
	if info.ModName != "eMule" || info.StrModVersion != "v0.70a" {
		t.Fatalf("PeerInfo client = %q %q", info.ModName, info.StrModVersion)
	}
}

// A mod that sends its name in CT_MOD_NAME (0x55) must win over CT_NAME.
func TestApplyClientTagsPrefersModTag(t *testing.T) {
	p := &PeerConnection{}
	p.applyClientTags(protocol.TagList{
		{Type: 23, ID: tagClientName, String: "eServer"},
		{Type: 23, ID: tagModName, String: "eserver"},
	})
	if p.remotePeerInfo.ModName != "eserver" {
		t.Fatalf("client = %q, want eserver", p.remotePeerInfo.ModName)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("abcdef", 4); got != "abcd…" {
		t.Fatalf("got %q", got)
	}
	// Multi-byte runes must not be sliced in half.
	if got := truncateRunes("中文客户端名称", 3); got != "中文客…" {
		t.Fatalf("got %q", got)
	}
	if got := truncateRunes("short", 40); got != "short" {
		t.Fatalf("got %q", got)
	}
}

// Go gives << and * the same precedence. Without explicit parentheses the
// capabilities in CT_EMULE_MISCOPTIONS1 land in the wrong nibble, and peers
// treat us as a client that cannot speak Unicode or source exchange.
func TestMiscOptionsBitPositions(t *testing.T) {
	options := MiscOptions{
		UnicodeSupport:     1,
		DataCompVer:        2,
		SourceExchange1Ver: 3,
		NoViewSharedFiles:  1,
	}
	want := (1 << 28) | (2 << 20) | (3 << 12) | (1 << 2)
	if got := options.IntValue(); got != want {
		t.Fatalf("IntValue=%#x, want %#x", got, want)
	}
	var decoded MiscOptions
	decoded.Assign(want)
	if decoded.UnicodeSupport != 1 || decoded.DataCompVer != 2 ||
		decoded.SourceExchange1Ver != 3 || decoded.NoViewSharedFiles != 1 {
		t.Fatalf("Assign(%#x) = %+v", want, decoded)
	}
}
