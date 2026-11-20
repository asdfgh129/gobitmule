package dashtransport

import (
	"errors"
	"testing"
)

func TestParseMPDDurationTemplate(t *testing.T) {
	raw := []byte(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S"
     profiles="urn:mpeg:dash:profile:isoff-live:2012">
  <BaseURL>videos/</BaseURL>
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="v1" bandwidth="1000000" codecs="avc1.640029">
        <SegmentTemplate timescale="90000" duration="90000" startNumber="1"
                         initialization="init-$RepresentationID$.m4s" media="seg-$Number%05d$.m4s"/>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`)
	m, err := ParseMPD("https://cdn.example.com/live/manifest.mpd", raw)
	if err != nil {
		t.Fatalf("ParseMPD: %v", err)
	}
	if m.IsDynamic {
		t.Fatal("static MPD must not be marked dynamic")
	}
	if m.MediaPresentationDuration != 10 {
		t.Fatalf("duration = %v, want 10", m.MediaPresentationDuration)
	}
	rep := m.SelectRepresentation()
	if rep.ID != "v1" || rep.Bandwidth != 1000000 {
		t.Fatalf("representation = %+v", rep)
	}
	initURL, segs, err := m.ExpandSegmentURLs("https://cdn.example.com/live/manifest.mpd", rep)
	if err != nil {
		t.Fatalf("ExpandSegmentURLs: %v", err)
	}
	// BaseURL "videos/" + template: init resolves against the chain.
	if initURL != "https://cdn.example.com/live/videos/init-v1.m4s" {
		t.Fatalf("init URL = %s", initURL)
	}
	// PT10S at 90000/900000 per segment → 10 segments, numbered 1..10.
	if len(segs) != 10 {
		t.Fatalf("segments = %d, want 10", len(segs))
	}
	if segs[0] != "https://cdn.example.com/live/videos/seg-00001.m4s" {
		t.Fatalf("seg0 = %s", segs[0])
	}
	if segs[9] != "https://cdn.example.com/live/videos/seg-00010.m4s" {
		t.Fatalf("seg9 = %s", segs[9])
	}
}

func TestParseMPDTimelineTimeAddressing(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="1000" initialization="i.m4s" media="$Time$.m4s">
        <SegmentTimeline><S t="0" d="4000"/><S d="1000" r="5"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="a" bandwidth="500"/>
      <Representation id="b" bandwidth="900"/>
    </AdaptationSet>
  </Period>
</MPD>`)
	m, err := ParseMPD("https://a.example.com/x.mpd", raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.SelectRepresentation().ID != "b" {
		t.Fatalf("selection picked %s, want b (higher bandwidth)", m.SelectRepresentation().ID)
	}
	rep := m.SelectRepresentation()
	_, segs, err := m.ExpandSegmentURLs("https://a.example.com/x.mpd", rep)
	if err != nil {
		t.Fatal(err)
	}
	// S t=0 d=4000 → one segment at 0; S d=1000 r=5 → six segments 4000..9000.
	if len(segs) != 7 {
		t.Fatalf("segments = %d, want 7", len(segs))
	}
	want := []string{"0", "4000", "5000", "6000", "7000", "8000", "9000"}
	for i, w := range want {
		if !endsWith(segs[i], "/"+w+".m4s") {
			t.Fatalf("seg%d = %s, want ending %s.m4s", i, segs[i], w)
		}
	}
}

func TestParseMPDTemplateInheritance(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT4S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="1000" duration="1000" media="set-$Number$.m4s" initialization="set-init.m4s"/>
      <Representation id="lo" bandwidth="100"><SegmentTemplate media="lo-$Number$.m4s"/></Representation>
      <Representation id="hi" bandwidth="800"/>
    </AdaptationSet>
  </Period>
</MPD>`)
	m, err := ParseMPD("https://a.example.com/x.mpd", raw)
	if err != nil {
		t.Fatal(err)
	}
	set := &m.Period.AdaptationSets[0]
	// lo overrides media only: duration/timescale/init inherit from the set.
	lo := &set.Representations[0]
	_, segs, err := m.ExpandSegmentURLs("https://a.example.com/x.mpd", lo)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 4 || !endsWith(segs[0], "/lo-0.m4s") {
		t.Fatalf("lo segments = %v", segs)
	}
	// hi inherits everything from the set.
	hi := &set.Representations[1]
	_, segs2, err := m.ExpandSegmentURLs("https://a.example.com/x.mpd", hi)
	if err != nil {
		t.Fatal(err)
	}
	if !endsWith(segs2[0], "/set-0.m4s") {
		t.Fatalf("hi segments = %v", segs2)
	}
}

func TestParseMPDSegmentList(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT5S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v" bandwidth="100">
        <SegmentList timescale="1000" duration="1000">
          <Initialization sourceURL="init.mp4"/>
          <SegmentURL media="s1.m4s"/>
          <SegmentURL sourceURL="http://other.example.com/s2.m4s"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`)
	m, err := ParseMPD("https://a.example.com/x.mpd", raw)
	if err != nil {
		t.Fatal(err)
	}
	rep := m.SelectRepresentation()
	initURL, segs, err := m.ExpandSegmentURLs("https://a.example.com/x.mpd", rep)
	if err != nil {
		t.Fatal(err)
	}
	if initURL != "https://a.example.com/init.mp4" {
		t.Fatalf("init = %s", initURL)
	}
	if len(segs) != 2 || !endsWith(segs[0], "/s1.m4s") || segs[1] != "http://other.example.com/s2.m4s" {
		t.Fatalf("segments = %v", segs)
	}
}

func TestParseMPDRejectsInvalidDocuments(t *testing.T) {
	base := "https://a.example.com/x.mpd"
	cases := map[string]string{
		"dynamic MPD": `<MPD type="dynamic"><Period><AdaptationSet><Representation id="a" bandwidth="1"><SegmentTemplate media="s$Number$.m4s" duration="1"/></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`,
		"two periods": `<MPD type="static"><Period/><Period/></MPD>`,
		"two adaptation sets": `<MPD type="static"><Period>
			<AdaptationSet contentType="video"><Representation id="v" bandwidth="1"><SegmentTemplate media="v$Number$.m4s" duration="1"/></SegmentTemplate></Representation></AdaptationSet>
			<AdaptationSet contentType="audio"><Representation id="a" bandwidth="1"><SegmentTemplate media="a$Number$.m4s" duration="1"/></SegmentTemplate></Representation></AdaptationSet>
		</Period></MPD>`,
		"DRM": `<MPD type="static"><Period><AdaptationSet><ContentProtection/><Representation id="v" bandwidth="1"><SegmentTemplate media="v$Number$.m4s" duration="1"/></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`,
		"segment base": `<MPD type="static"><Period><AdaptationSet><Representation id="v" bandwidth="1"><BaseURL>v.mp4</BaseURL><SegmentBase indexRange="0-99"/></Representation></AdaptationSet></Period></MPD>`,
		"unbounded duration template": `<MPD type="static"><Period><AdaptationSet><Representation id="v" bandwidth="1"><SegmentTemplate media="v$Number$.m4s" duration="1"/></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`,
		"time without timeline": `<MPD type="static" mediaPresentationDuration="PT4S"><Period><AdaptationSet><Representation id="v" bandwidth="1"><SegmentTemplate media="$Time$.m4s" duration="1"/></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`,
		"DTD":                     `<!DOCTYPE MPD [<!ENTITY x SYSTEM "file:///etc/passwd">]><MPD type="static">&x;</MPD>`,
		"no representations":      `<MPD type="static"><Period><AdaptationSet/></Period></MPD>`,
		"calendar duration":       `<MPD type="static" mediaPresentationDuration="P1YT30S"><Period><AdaptationSet><Representation id="v" bandwidth="1"><SegmentTemplate media="v$Number$.m4s" duration="1"/></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`,
	}
	for name, raw := range cases {
		if _, err := ParseMPD(base, []byte(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseMPDErrorSentinels(t *testing.T) {
	dynamic := []byte(`<MPD type="dynamic"/>`)
	if _, err := ParseMPD("u", dynamic); !errors.Is(err, ErrDynamicNotSupport) {
		t.Fatalf("dynamic err = %v", err)
	}
	if _, err := ParseMPD("u", []byte(`<MPD type="static"><Period/><Period/></MPD>`)); !errors.Is(err, ErrMultiPeriod) {
		t.Fatalf("multi-period err = %v", err)
	}
}

func TestParseISODuration(t *testing.T) {
	cases := map[string]float64{
		"PT10S":     10,
		"PT1M30S":   90,
		"PT1H2M3.5S": 3723.5,
		"P1D":       86400,
		"pt4s":      4, // case-insensitive per spec
	}
	for in, want := range cases {
		got, err := parseISODuration(in)
		if err != nil || got != want {
			t.Errorf("parseISODuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseISODuration("P1Y"); err == nil {
		t.Error("P1Y (calendar) must be rejected")
	}
	if _, err := parseISODuration("10S"); err == nil {
		t.Error("missing P must be rejected")
	}
	if _, err := parseISODuration("PT"); err == nil {
		t.Error("empty time component must be rejected")
	}
}

func TestExpandTemplateIdentifiers(t *testing.T) {
	cases := []struct{ tpl, want string }{
		{"$RepresentationID$/$Number$.m4s", "v1/5.m4s"},
		{"$Number%05d$.m4s", "00005.m4s"},
		{"$Bandwidth$_$Time$.m4s", "900_42.m4s"},
		{"$$x$Number$.m4s", "$x5.m4s"},
		{"init-$RepresentationID$.m4s", "init-v1.m4s"},
	}
	for _, c := range cases {
		if got := expandTemplate(c.tpl, "v1", 900, 5, 42); got != c.want {
			t.Errorf("expandTemplate(%q) = %q, want %q", c.tpl, got, c.want)
		}
	}
}

func TestLooksLikeMPD(t *testing.T) {
	if !LooksLikeMPD([]byte(`<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash"/>`)) {
		t.Fatal("plain MPD rejected")
	}
	if LooksLikeMPD([]byte("#EXTM3U")) {
		t.Fatal("HLS playlist accepted as MPD")
	}
	if LooksLikeMPD([]byte(`<?xml version="1.0"?><metalink/>`)) {
		t.Fatal("non-MPD XML accepted")
	}
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
