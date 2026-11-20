package postproc

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

// --- synthetic MPEG-TS fixtures (no ffmpeg dependency in CI) ---

func tsPkt(pid uint16, pusi bool, payload []byte) []byte {
	p := make([]byte, tsPacketSize)
	p[0] = 0x47
	p[1] = byte(pid>>8) & 0x1f
	p[2] = byte(pid)
	p[3] = 0x10
	if pusi {
		p[1] |= 0x40
	}
	copy(p[4:], payload)
	return p
}

func tsPAT() []byte {
	// pointer=0, table_id=0, section_length=13, tsid, version, section numbers,
	// program 1 -> PMT pid 4096, CRC stub.
	sec := []byte{0, 0x00, 0xb0, 0x0d, 0, 1, 0xc1, 0, 0, 0, 1, 0xf0, 0x00, 0, 0, 0, 0}
	return tsPkt(0, true, sec)
}

func tsPMT() []byte {
	// pointer=0, table_id=2, section_length=23, program 1, PCR pid 256,
	// program_info_length=0; H.264 pid 256, AAC pid 257; CRC stub.
	sec := []byte{
		0, 0x02, 0xb0, 0x17, 0, 1, 0xc1, 0, 0, 0x01, 0x00, 0x00, 0x00,
		0x1b, 0x01, 0x00, 0x00, 0x00,
		0x0f, 0x01, 0x01, 0x00, 0x00,
		0, 0, 0, 0,
	}
	return tsPkt(4096, true, sec)
}

// Minimal SPS: baseline profile, 16x16 macroblock, no cropping. Its exact
// dimensions are not important for structural tests; only that avcC carries it.
var fakeSPS = []byte{0x67, 0x42, 0x00, 0x0a, 0xf4, 0xf3}
var fakePPS = []byte{0x68, 0xce, 0x06, 0xe2}

func pes(pid uint16, pts, dts int64, payload []byte) []byte {
	// PES optional header: flags1=0x80, flags2=0x80 (PTS only), header_len=5.
	header := []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5}
	v := uint64(pts) & ((1 << 33) - 1)
	p := []byte{
		0x21 | byte((v>>29)&0x0e), byte(v >> 22), 0x01 | byte((v>>14)&0xfe),
		byte(v >> 7), 0x01 | byte((v<<1)&0xfe),
	}
	_ = dts // synthetic fixtures carry PTS only
	header = append(header, p...)
	plen := len(header) - 6 + len(payload) // bytes after the length field
	header[4], header[5] = byte(plen>>8), byte(plen)
	out := append(header, payload...)
	var buf bytes.Buffer
	buf.Write(tsPkt(pid, true, out[:min(184, len(out))]))
	for off := 184; off < len(out); off += 184 {
		buf.Write(tsPkt(pid, false, out[off:min(off+184, len(out))]))
	}
	return buf.Bytes()
}

// pesWithDTS emits a PES carrying both PTS and DTS, exercising ctts.
func pesWithDTS(pid uint16, pts, dts int64, payload []byte) []byte {
	v := uint64(pts) & ((1 << 33) - 1)
	hdr := []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0xc0, 10,
		0x21 | byte((v>>29)&0x0e), byte(v >> 22), 0x01 | byte((v>>14)&0xfe), byte(v >> 7), 0x01 | byte((v<<1)&0xfe)}
	d := uint64(dts) & ((1 << 33) - 1)
	hdr = append(hdr, 0x11|byte((d>>29)&0x0e), byte(d>>22), 0x01|byte((d>>14)&0xfe), byte(d>>7), 0x01|byte((d<<1)&0xfe))
	plen := len(hdr) - 6 + len(payload)
	hdr[4], hdr[5] = byte(plen>>8), byte(plen)
	out := append(hdr, payload...)
	var buf bytes.Buffer
	buf.Write(tsPkt(pid, true, out[:min(184, len(out))]))
	for off := 184; off < len(out); off += 184 {
		buf.Write(tsPkt(pid, false, out[off:min(off+184, len(out))]))
	}
	return buf.Bytes()
}

func adtsFrame(payload []byte) []byte {
	// MPEG-4 AAC LC, 48000 Hz, mono, no CRC, frameLen = 7 + payload.
	n := len(payload) + 7
	out := []byte{0xff, 0xf1, 0x4c, 0x40, byte(n >> 3), byte((n & 7) << 5), 0xff}
	return append(out, payload...)
}

func syntheticTS() []byte {
	var b bytes.Buffer
	b.Write(tsPAT())
	b.Write(tsPMT())
	avc := append([]byte{0, 0, 0, 1}, fakeSPS...)
	avc = append(avc, 0, 0, 0, 1)
	avc = append(avc, fakePPS...)
	avc = append(avc, 0, 0, 0, 1, 0x65, 0x88, 0x84, 0x21, 0xa0)
	b.Write(pesWithDTS(256, 100000, 90000, avc))
	avc2 := []byte{0, 0, 0, 1, 0x41, 0x9a, 0x20, 0x80}
	b.Write(pes(256, 180000, -1, avc2))
	a1 := append(adtsFrame([]byte{0xde, 0x02, 0x00, 0x4c}), adtsFrame([]byte{0xde, 0x03, 0x00, 0x4d})...)
	b.Write(pes(257, 90000, -1, a1))
	return b.Bytes()
}

func findBox(b []byte, typ string, from int) (int, int, bool) {
	for off := from; off+8 <= len(b); {
		size := int(binary.BigEndian.Uint32(b[off : off+4]))
		if size < 8 || off+size > len(b) {
			return 0, 0, false
		}
		if string(b[off+4:off+8]) == typ {
			return off, size, true
		}
		off += size
	}
	return 0, 0, false
}

func TestRemuxSyntheticTS(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "input.ts")
	if err := os.WriteFile(in, syntheticTS(), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.mp4")
	res, err := (Remux{}).Process(context.Background(),
		core.LocalFiles{BaseDir: dir, Paths: []string{in}},
		core.OutputSpec{Target: out, TempSuffix: ".part"})
	if err != nil {
		t.Fatalf("remux: %v", err)
	}
	want := out + ".remux.part"
	if len(res.Outputs) != 1 || res.Outputs[0] != want {
		t.Fatalf("outputs = %v, want %v", res.Outputs, want)
	}
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"ftyp", "moov", "mdat"} {
		if _, _, ok := findBox(b, typ, 0); !ok {
			t.Errorf("missing %s box", typ)
		}
	}
	moovOff, moovSize, _ := findBox(b, "moov", 0)
	mdatOff, _, _ := findBox(b, "mdat", 0)
	if moovOff+moovSize != mdatOff {
		t.Errorf("moov is not before mdat (faststart): moov %d+%d, mdat %d", moovOff, moovSize, mdatOff)
	}
	if !bytes.Contains(b, []byte("avc1")) || !bytes.Contains(b, []byte("mp4a")) {
		t.Error("missing avc1/mp4a sample entries")
	}
	if !bytes.Contains(b, []byte("ctts")) {
		t.Error("missing ctts box for composition offsets")
	}
}

func TestRemuxRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "bad.ts")
	_ = os.WriteFile(in, []byte("not a transport stream"), 0o600)
	cases := []struct {
		name string
		in   core.LocalFiles
		out  core.OutputSpec
	}{
		{"no inputs", core.LocalFiles{}, core.OutputSpec{Target: filepath.Join(dir, "o.mp4")}},
		{"remote path", core.LocalFiles{Paths: []string{"https://example.com/a.ts"}}, core.OutputSpec{Target: filepath.Join(dir, "o.mp4")}},
		{"bad ts", core.LocalFiles{Paths: []string{in}}, core.OutputSpec{Target: filepath.Join(dir, "o.mp4")}},
		{"relative target", core.LocalFiles{Paths: []string{in}}, core.OutputSpec{Target: "o.mp4"}},
		{"bad suffix", core.LocalFiles{Paths: []string{in}}, core.OutputSpec{Target: filepath.Join(dir, "o.mp4"), TempSuffix: "../x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (Remux{}).Process(context.Background(), tc.in, tc.out); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRemuxRebaseMonotonic(t *testing.T) {
	samples := []sample{
		{data: []byte{1}, dts: 100, cts: 40},
		{data: []byte{2}, dts: 90, cts: 0}, // out of order + backward DTS
		{data: []byte{3}, dts: 110, cts: 20},
	}
	rebase(samples, true)
	for i := 1; i < len(samples); i++ {
		if samples[i].dts <= samples[i-1].dts {
			t.Fatalf("DTS not monotonic at %d: %+v", i, samples)
		}
	}
	if samples[0].dts != 0 {
		t.Fatalf("first DTS = %d, want 0", samples[0].dts)
	}
	for _, s := range samples {
		if s.duration == 0 {
			t.Error("zero duration")
		}
	}
}

func TestTimestampUnwrapWraparound(t *testing.T) {
	const mod = int64(1) << 33
	got := unwrap90(5, mod-100)
	if got <= mod-100 {
		t.Fatalf("wrap not unwrapped: %d (ref %d)", got, mod-100)
	}
	// A raw value just below the modulus, with a small reference, is the
	// negative timestamp immediately before the epoch.
	if unwrap90(mod-1, 10) != -1 {
		t.Fatalf("wrap-before-epoch = %d, want -1", unwrap90(mod-1, 10))
	}
}

func TestSplitAnnexBAndAVCC(t *testing.T) {
	data := []byte{0, 0, 0, 1, 0x67, 1, 2, 3, 0, 0, 1, 0x68, 9, 9, 0, 0, 1, 0x65, 7, 7}
	cfg := &avcConfig{}
	out, err := annexBToAVCC(data, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Only the VCL NAL (0x65 07 07) remains after SPS/PPS are moved to avcC.
	if len(out) != 4+3 {
		t.Fatalf("AVCC len = %d", len(out))
	}
	if binary.BigEndian.Uint32(out[:4]) != 3 {
		t.Fatalf("first NAL length = %d", binary.BigEndian.Uint32(out[:4]))
	}
	if cfg.avcC == nil || string(cfg.avcC[4:8]) != "avcC" {
		t.Fatal("avcC not built")
	}
}

func TestSPSDimensions(t *testing.T) {
	w, h, err := spsDimensions(fakeSPS)
	if err != nil {
		t.Fatal(err)
	}
	if w == 0 || h == 0 || w > 4096 || h > 4096 {
		t.Fatalf("dimensions = %dx%d", w, h)
	}
}

func TestAdtsToSamples(t *testing.T) {
	cfg := &aacConfig{}
	frames, err := adtsToSamples(append(adtsFrame([]byte{1, 2, 3}), adtsFrame([]byte{4, 5, 6})...), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || len(frames[0]) != 3 {
		t.Fatalf("frames = %d, first len = %d", len(frames), len(frames[0]))
	}
	if cfg.rate != 48000 || cfg.channels != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}
}
