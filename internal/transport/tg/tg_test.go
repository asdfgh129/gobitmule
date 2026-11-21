package tgtransport

// Offline tests for the Telegram transport (WBS 15, ADR-0029): link parsing,
// seam-injected media, sequential commit through the ChunkWriter contract,
// FLOOD_WAIT classification and session requirements. Real MTProto wire
// traffic is NOT_EXECUTED (`12` §1 — CI never touches Telegram).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gotd/td/tgerr"

	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/security"
)

type fakeMedia struct {
	statSize  int64
	statName  string
	statErr   error
	streamErr error
	body      []byte
}

func (f *fakeMedia) Stat(_ context.Context, _ LinkRef) (int64, string, error) {
	return f.statSize, f.statName, f.statErr
}

func (f *fakeMedia) Stream(_ context.Context, _ LinkRef, w io.Writer) error {
	if f.streamErr != nil {
		return f.streamErr
	}
	_, err := w.Write(f.body)
	return err
}

// testWriter is the scheduler-owned ChunkWriter stand-in (same shape as the
// ftp/hls/dash test doubles).
type testWriter struct {
	chunks  []core.Chunk
	written map[int64][]byte
}

func newTestWriter(size, chunkSize int64) *testWriter {
	var chunks []core.Chunk
	for start, idx := int64(0), 0; start < size; start, idx = start+chunkSize, idx+1 {
		n := chunkSize
		if size-start < chunkSize {
			n = size - start
		}
		chunks = append(chunks, core.Chunk{Index: idx, Start: start, Length: n, State: core.ChunkPending})
	}
	return &testWriter{chunks: chunks, written: map[int64][]byte{}}
}

func (w *testWriter) Next(context.Context) (core.Chunk, bool, error) {
	for i := range w.chunks {
		if w.chunks[i].State == core.ChunkPending {
			w.chunks[i].State = core.ChunkAssigned
			return w.chunks[i], true, nil
		}
	}
	return core.Chunk{}, false, nil
}
func (w *testWriter) WriteChunk(_ context.Context, c core.Chunk, p []byte) error {
	w.written[int64(c.Index)] = append([]byte(nil), p...)
	for i := range w.chunks {
		if w.chunks[i].Index == c.Index {
			w.chunks[i].State = core.ChunkDone
		}
	}
	return nil
}
func (w *testWriter) Verified(context.Context, core.Chunk, core.Hash) error { return nil }
func (w *testWriter) Release(context.Context, core.Chunk, error) error      { return nil }
func (w *testWriter) concatenation() []byte {
	var out []byte
	for i := 0; i < len(w.chunks); i++ {
		out = append(out, w.written[int64(i)]...)
	}
	return out
}

type countingBudget struct{ bytes int }

func (b *countingBudget) Consume(_ context.Context, n int) error {
	b.bytes += n
	return nil
}

func TestParseLinkShapes(t *testing.T) {
	yes := []struct {
		raw  string
		want LinkRef
	}{
		{"https://t.me/durov/42", LinkRef{Channel: "durov", MsgID: 42}},
		{"https://telegram.me/durov/42", LinkRef{Channel: "durov", MsgID: 42}},
		{"tg://resolve?domain=durov&post=42", LinkRef{Channel: "durov", MsgID: 42}},
		{"https://t.me/c/1789123456/77", LinkRef{Channel: "1789123456", MsgID: 77, Private: true}},
		{"tg://privatepost?channel=1789123456&post=77", LinkRef{Channel: "1789123456", MsgID: 77, Private: true}},
	}
	for _, tc := range yes {
		got, err := ParseLink(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("ParseLink(%q) = %+v, %v; want %+v", tc.raw, got, err, tc.want)
		}
	}
	no := []string{
		"https://t.me/durov",        // no message id
		"https://t.me/s/durov",      // web preview
		"https://example.com/x/1",   // wrong host
		"tg://resolve?domain=durov", // no post
		"https://t.me/c/abc/5",      // non-numeric private id
		"https://t.me/durov/0",      // non-positive id
		"",                          // empty
		"https://t.me/" + strings.Repeat("x", 500) + "/1", // oversized
	}
	for _, raw := range no {
		if _, err := ParseLink(raw); err == nil {
			t.Errorf("ParseLink(%q) must fail", raw)
		}
	}
}

func TestNormalizeRoundTrip(t *testing.T) {
	for _, ref := range []LinkRef{
		{Channel: "durov", MsgID: 42},
		{Channel: "1789123456", MsgID: 77, Private: true},
	} {
		mirror := Normalize(ref)
		if !strings.HasPrefix(mirror, "tg://ud/") {
			t.Fatalf("mirror = %q, want the internal tg://ud form", mirror)
		}
		back, err := ParseNormalized(mirror)
		if err != nil || back != ref {
			t.Fatalf("round trip: %+v, %v", back, err)
		}
	}
	if _, err := ParseNormalized("https://t.me/durov/42"); err == nil {
		t.Fatal("non-internal URL must not parse as a mirror")
	}
}

func TestTransportSupportsOnlyInternalMirrors(t *testing.T) {
	tr := New(security.NewMemoryCredentialStore(), 1, "h", 0)
	if tr.Supports(&core.Resource{Mirrors: []core.Mirror{{URL: "https://t.me/x/1"}}}) {
		t.Fatal("https mirrors are not telegram resources")
	}
	// The internal tg://ud mirror form is what dispatches here (no http(s)
	// transport can claim it) — no identity marker needed.
	if !tr.Supports(&core.Resource{Mirrors: []core.Mirror{{URL: "tg://ud/link?domain=x&post=1"}}}) {
		t.Fatal("internal tg://ud mirrors must be supported")
	}
	if tr.Supports(nil) {
		t.Fatal("nil must not be supported")
	}
}

func TestProbeRejectsMissingSession(t *testing.T) {
	// Production path without a session credential: clean capability refusal
	// BEFORE any network attempt.
	tr := New(security.NewMemoryCredentialStore(), 1, "h", 0)
	res := &core.Resource{
		Identity:   core.ProtocolIdentity{VariantID: "tg:x/1"},
		Mirrors:    []core.Mirror{{URL: "tg://ud/link?domain=x&post=1"}},
		Credential: "",
	}
	_, err := tr.Probe(context.Background(), res)
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("err = %v, want the session requirement", err)
	}
}

func TestRunStreamsThroughChunkPlanAndBudget(t *testing.T) {
	body := bytes.Repeat([]byte{0x41, 0x42}, 2048) // 4096 bytes
	fake := &fakeMedia{statSize: int64(len(body)), statName: "clip.mp4", body: body}
	tr := newTransportWithMedia(security.NewMemoryCredentialStore(), fake)
	res := &core.Resource{
		Identity:   core.ProtocolIdentity{VariantID: "tg:chan/1"},
		Mirrors:    []core.Mirror{{URL: Normalize(LinkRef{Channel: "chan", MsgID: 1})}},
		Credential: "mem://ud/cred/x",
	}
	p, err := tr.Probe(context.Background(), res)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if p.Size != int64(len(body)) || p.Filename != "clip.mp4" {
		t.Fatalf("probe = %+v", p)
	}
	w := newTestWriter(int64(len(body)), 1000)
	bg := &countingBudget{}
	if err := tr.Run(context.Background(), res, w, bg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !bytes.Equal(w.concatenation(), body) {
		t.Fatalf("streamed %d bytes, want exact %d", len(w.concatenation()), len(body))
	}
	if bg.bytes != len(body) {
		t.Fatalf("budget charged %d, want %d", bg.bytes, len(body))
	}
}

func TestFloodWaitClassifiedAsTransient(t *testing.T) {
	flood := tgerr.New(400, "FLOOD_WAIT")
	flood.Argument = 42
	fake := &fakeMedia{statErr: flood}
	tr := newTransportWithMedia(security.NewMemoryCredentialStore(), fake)
	res := &core.Resource{
		Identity:   core.ProtocolIdentity{VariantID: "tg:chan/1"},
		Mirrors:    []core.Mirror{{URL: Normalize(LinkRef{Channel: "chan", MsgID: 1})}},
		Credential: "mem://ud/cred/x",
	}
	_, err := tr.Probe(context.Background(), res)
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") {
		t.Fatalf("err = %v, want a classified FLOOD_WAIT", err)
	}
	if !strings.Contains(err.Error(), "42s") {
		t.Fatalf("err = %v, want the server-given 42s wait", err)
	}
}

func TestPrivateLinkRefused(t *testing.T) {
	fake := &fakeMedia{statErr: ErrPrivateLinks}
	tr := newTransportWithMedia(security.NewMemoryCredentialStore(), fake)
	res := &core.Resource{
		Identity:   core.ProtocolIdentity{VariantID: "tg:1789123456/1"},
		Mirrors:    []core.Mirror{{URL: Normalize(LinkRef{Channel: "1789123456", MsgID: 1, Private: true})}},
		Credential: "mem://ud/cred/x",
	}
	_, err := tr.Probe(context.Background(), res)
	if err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("err = %v, want the private-channel refusal", err)
	}
	_ = errors.Is
}

func TestRunRejectsBadKind(t *testing.T) {
	tr := newTransportWithMedia(security.NewMemoryCredentialStore(), &fakeMedia{})
	res := &core.Resource{
		Kind:     core.KindTorrent,
		Identity: core.ProtocolIdentity{VariantID: "tg:x/1"},
		Mirrors:  []core.Mirror{{URL: "tg://ud/link?domain=x&post=1"}},
	}
	if err := tr.Run(context.Background(), res, newTestWriter(1, 1), nil); err == nil {
		t.Fatal("Run must reject non-tg resources")
	}
}
