package hlstransport

import (
	"errors"
	"testing"
)

func TestParseMediaPlaylistBasic(t *testing.T) {
	raw := []byte("#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:10\n" +
		"#EXT-X-MEDIA-SEQUENCE:7\n" +
		"#EXTINF:9.009,title one\n" +
		"seg0.ts\n" +
		"#EXTINF:9.009,\n" +
		"seg1.ts\n" +
		"#EXT-X-ENDLIST\n")
	pl, err := ParsePlaylist("https://cdn.example.com/live/media.m3u8", raw)
	if err != nil {
		t.Fatalf("ParsePlaylist: %v", err)
	}
	if pl.Media == nil || pl.Master != nil {
		t.Fatalf("expected media playlist, got %+v", pl)
	}
	m := pl.Media
	if !m.VOD || m.TargetDur != 10 || m.MediaSeq != 7 {
		t.Fatalf("playlist header mismatch: %+v", m)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(m.Segments))
	}
	s0 := m.Segments[0]
	if s0.URI != "https://cdn.example.com/live/seg0.ts" {
		t.Fatalf("relative URI not resolved: %s", s0.URI)
	}
	if s0.Duration != 9.009 || s0.Title != "title one" {
		t.Fatalf("EXTINF mismatch: %+v", s0)
	}
	if s0.Key != nil {
		t.Fatalf("unencrypted playlist must not carry a key: %+v", s0.Key)
	}
	if sequenceOf(m, 1) != 8 {
		t.Fatalf("media sequence of seg1 = %d, want 8", sequenceOf(m, 1))
	}
}

func TestParseMediaPlaylistAbsoluteAndQueryURIs(t *testing.T) {
	raw := []byte("#EXTM3U\n#EXTINF:1,\nhttps://other.example.com/x.ts?token=abc\n#EXT-X-ENDLIST\n")
	pl, err := ParsePlaylist("https://a.example.com/p.m3u8", raw)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Media.Segments[0].URI != "https://other.example.com/x.ts?token=abc" {
		t.Fatalf("absolute URI rewritten: %s", pl.Media.Segments[0].URI)
	}
}

func TestParseMediaPlaylistKeysAndIV(t *testing.T) {
	raw := []byte("#EXTM3U\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"keys/k1\",IV=0x000102030405060708090a0b0c0d0e0f\n" +
		"#EXTINF:1,\na.ts\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"keys/k2\"\n" +
		"#EXTINF:1,\nb.ts\n" +
		"#EXT-X-KEY:METHOD=NONE\n" +
		"#EXTINF:1,\nc.ts\n" +
		"#EXT-X-ENDLIST\n")
	pl, err := ParsePlaylist("https://cdn.example.com/m.m3u8", raw)
	if err != nil {
		t.Fatal(err)
	}
	segs := pl.Media.Segments
	if segs[0].Key == nil || segs[0].Key.URI != "https://cdn.example.com/keys/k1" {
		t.Fatalf("seg0 key = %+v", segs[0].Key)
	}
	if segs[0].Key.IV != "000102030405060708090a0b0c0d0e0f" {
		t.Fatalf("IV prefix not stripped: %q", segs[0].Key.IV)
	}
	if segs[1].Key == nil || segs[1].Key.URI != "https://cdn.example.com/keys/k2" || segs[1].Key.IV != "" {
		t.Fatalf("seg1 key = %+v", segs[1].Key)
	}
	if segs[2].Key != nil {
		t.Fatalf("METHOD=NONE must clear the key: %+v", segs[2].Key)
	}
}

func TestParseMediaPlaylistRejectsBadKeys(t *testing.T) {
	cases := map[string]string{
		"key without URI":   "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n",
		"sample-aes":        "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k\"\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n",
		"unknown method":    "#EXTM3U\n#EXT-X-KEY:METHOD=CHACHA20,URI=\"k\"\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n",
		"bad byterange":     "#EXTM3U\n#EXT-X-BYTERANGE:abc@0\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n",
		"negative range":    "#EXTM3U\n#EXT-X-BYTERANGE:-5@0\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n",
		"no segments":       "#EXTM3U\n#EXT-X-ENDLIST\n",
		"not a playlist":    "hello world\n",
		"mixed document":    "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\na.m3u8\n#EXTINF:1,\nb.ts\n",
		"master no variant": "#EXTM3U\n",
	}
	for name, raw := range cases {
		if _, err := ParsePlaylist("https://x.example.com/p.m3u8", []byte(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParsePlaylistErrorsAreSentinels(t *testing.T) {
	if _, err := ParsePlaylist("u", []byte("plain text")); !errors.Is(err, ErrNotPlaylist) {
		t.Fatalf("err = %v, want ErrNotPlaylist", err)
	}
	// A document with the magic but neither STREAM-INF nor EXTINF is not a
	// recognizable playlist type for this transport.
	if _, err := ParsePlaylist("u", []byte("#EXTM3U\n")); !errors.Is(err, ErrNotPlaylist) {
		t.Fatalf("empty document err = %v", err)
	}
}

func TestParseMasterAndSelectVariant(t *testing.T) {
	raw := []byte("#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=500000,RESOLUTION=640x360,CODECS=\"avc1.640029,mp4a.40.2\"\nlow.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1920x1080\nhigh.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720\nmid.m3u8\n" +
		"#EXT-X-STREAM-INF:RESOLUTION=480x270\nfallback.m3u8\n")
	pl, err := ParsePlaylist("https://cdn.example.com/master.m3u8", raw)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Master == nil || len(pl.Master.Variants) != 4 {
		t.Fatalf("master = %+v", pl.Master)
	}
	v := pl.Master.SelectVariant()
	// Ties fall back to document order: high.m3u8 wins over the equal mid.
	if v.URI != "https://cdn.example.com/high.m3u8" {
		t.Fatalf("selected %s, want high.m3u8", v.URI)
	}
	if v.Resolution != "1920x1080" {
		t.Fatalf("attributes lost: %+v", v)
	}
	if pl.Master.Variants[0].Codecs != "avc1.640029,mp4a.40.2" {
		t.Fatalf("quoted CODECS lost: %+v", pl.Master.Variants[0])
	}
	// The extensionless fallback must rank below every declared bandwidth.
	if pl.Master.Variants[3].IsFallback != true {
		t.Fatal("variant without BANDWIDTH must be marked fallback")
	}
}

func TestParseMediaByteRangesAndFMP4(t *testing.T) {
	raw := []byte("#EXTM3U\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXT-X-BYTERANGE:1000@0\n" +
		"#EXTINF:1,\nmedia.m4s\n" +
		"#EXT-X-BYTERANGE:500\n" +
		"#EXTINF:1,\nmedia.m4s\n" +
		"#EXT-X-ENDLIST\n")
	pl, err := ParsePlaylist("https://cdn.example.com/f.mp4.m3u8", raw)
	if err != nil {
		t.Fatal(err)
	}
	m := pl.Media
	if !m.IsFMP4 {
		t.Fatal("EXT-X-MAP must mark the playlist fMP4")
	}
	if m.Segments[0].ByteRange == nil || m.Segments[0].ByteRange.Length != 1000 || m.Segments[0].ByteRange.Offset != 0 {
		t.Fatalf("seg0 range = %+v", m.Segments[0].ByteRange)
	}
	// Second range continues the previous extent: 0+1000 = 1000.
	if m.Segments[1].ByteRange == nil || m.Segments[1].ByteRange.Offset != 1000 || m.Segments[1].ByteRange.Length != 500 {
		t.Fatalf("seg1 range = %+v", m.Segments[1].ByteRange)
	}
}

func TestLooksLikePlaylist(t *testing.T) {
	if !LooksLikePlaylist([]byte("#EXTM3U\n")) {
		t.Fatal("plain magic rejected")
	}
	if !LooksLikePlaylist([]byte("\xef\xbb\xbf#EXTM3U\n")) {
		t.Fatal("BOM-prefixed magic rejected")
	}
	if LooksLikePlaylist([]byte("<html><body>not a playlist</body></html>")) {
		t.Fatal("HTML accepted")
	}
}

func TestDefaultName(t *testing.T) {
	cases := []struct {
		url  string
		fmp4 bool
		want string
	}{
		{"https://a.example.com/live/master.m3u8?tok=1", false, "master.ts"},
		{"https://a.example.com/live/master.m3u8", true, "master.mp4"},
		{"https://a.example.com/", false, "stream.ts"},
	}
	for _, c := range cases {
		if got := defaultName(c.url, c.fmp4); got != c.want {
			t.Errorf("defaultName(%q,%v) = %q, want %q", c.url, c.fmp4, got, c.want)
		}
	}
}

func TestSegmentAt(t *testing.T) {
	sizes := []int64{100, 200, 50}
	cases := []struct {
		off  int64
		want int
	}{
		{0, 0}, {99, 0}, {100, 1}, {299, 1}, {300, 2}, {349, 2}, {350, -1}, {-1, -1},
	}
	for _, c := range cases {
		if got := segmentAt(sizes, c.off); got != c.want {
			t.Errorf("segmentAt(%d) = %d, want %d", c.off, got, c.want)
		}
	}
}

func TestParseContentRangeTotal(t *testing.T) {
	if n, ok := parseContentRangeTotal("bytes 0-0/12345"); !ok || n != 12345 {
		t.Fatalf("got %d %v", n, ok)
	}
	if _, ok := parseContentRangeTotal("bytes 0-0/*"); ok {
		t.Fatal("unknown total must not parse")
	}
	if _, ok := parseContentRangeTotal("bytes 0-0/12x45"); ok {
		t.Fatal("garbage total must not parse")
	}
}
