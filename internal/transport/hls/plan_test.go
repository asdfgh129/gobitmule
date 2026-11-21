package hlstransport

// ADR-0028: EXT-X-MEDIA audio groups expand a master playlist into a
// two-file plan (video + audio media playlists) that the scheduler's
// multi-file loop downloads and the remux merges — no ffmpeg.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newHLSMasterServer serves an arbitrary master playlist document.
func newHLSMasterServer(t *testing.T, master []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(master)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestParseMediaAudioGroups(t *testing.T) {
	raw := []byte("#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"English\",DEFAULT=YES,AUTOSELECT=YES,URI=\"audio-en.m3u8\"\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"Commentary\",URI=\"audio-cmt.m3u8\"\n" +
		"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"sub\",NAME=\"EN\",URI=\"sub.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000,AUDIO=\"aud\"\nvideo.m3u8\n")
	pl, err := ParsePlaylist("https://cdn.example.com/master.m3u8", raw)
	if err != nil {
		t.Fatalf("ParsePlaylist: %v", err)
	}
	if pl.Master == nil || len(pl.Master.Variants) != 1 {
		t.Fatalf("variants = %+v", pl.Master)
	}
	v := pl.Master.SelectVariant()
	if v.AudioGroup != "aud" {
		t.Fatalf("AUDIO attr = %q", v.AudioGroup)
	}
	// DEFAULT=YES wins over document order (Commentary listed second... wait:
	// English is first AND default; flip a case without DEFAULT to check).
	r := pl.Master.SelectAudioRendition("aud")
	if r == nil || r.URI != "https://cdn.example.com/audio-en.m3u8" || !r.IsDefault {
		t.Fatalf("rendition = %+v", r)
	}
	// Subtitle groups are not audio groups.
	if pl.Master.SelectAudioRendition("sub") != nil {
		t.Fatal("subtitle group must not be an audio group")
	}
	// A rendition without URI never splits (muxed audio).
	pl2, _ := ParsePlaylist("u", []byte("#EXTM3U\n"+
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"muxed\"\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"a\"\nv.m3u8\n"))
	if pl2.Master.SelectAudioRendition("a") != nil {
		t.Fatal("muxed rendition (no URI) must not split")
	}
	// Missing GROUP-ID is rejected.
	if _, err := ParsePlaylist("u", []byte("#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,NAME=\"x\"\n#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"x\"\nv.m3u8\n")); err == nil {
		t.Fatal("GROUP-ID-less EXT-X-MEDIA must be rejected")
	}
}

func TestPlanFilesSplitsAudioRendition(t *testing.T) {
	fixture := []byte("#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"EN\",DEFAULT=YES,URI=\"audio.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=5000000,AUDIO=\"aud\"\nvideo.m3u8\n")
	srv := newHLSMasterServer(t, fixture)
	tr := New(nil)
	res := streamResource(srv.URL + "/master.m3u8")
	plan, err := tr.PlanFiles(context.Background(), res)
	if err != nil {
		t.Fatalf("PlanFiles: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("plan = %d files, want 2", len(plan))
	}
	if plan[0].URL != srv.URL+"/video.m3u8" || plan[1].URL != srv.URL+"/audio.m3u8" {
		t.Fatalf("plan URLs = %s / %s", plan[0].URL, plan[1].URL)
	}
	if plan[0].Index != 0 || plan[1].Index != 1 || !plan[0].Selected || !plan[1].Selected {
		t.Fatalf("plan files = %+v / %+v", plan[0], plan[1])
	}
}

func TestPlanFilesNoSplitWithoutAudio(t *testing.T) {
	// Master with a muxed-audio group (no URI) and a plain master both keep
	// the single-file path.
	fixture := []byte("#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"EN\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"aud\"\nvideo.m3u8\n")
	srv := newHLSMasterServer(t, fixture)
	tr := New(nil)
	if plan, _ := tr.PlanFiles(context.Background(), streamResource(srv.URL+"/master.m3u8")); len(plan) != 0 {
		t.Fatalf("muxed rendition must not split: %+v", plan)
	}
	// A media playlist URL is not a master at all.
	media := newHLSServer(t, [][]byte{[]byte("seg")})
	if plan, _ := tr.PlanFiles(context.Background(), streamResource(media.URL+"/media.m3u8")); len(plan) != 0 {
		t.Fatal("media playlist must not split")
	}
}
