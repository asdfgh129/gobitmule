package ed2ktransport

// Offline tests for the WBS 16 / ADR-0030 eD2k Transport skeleton (docs/25).
// All network/eD2k wire traffic is replaced by a Downloader seam; CI never
// touches the real eD2k network. These tests prove the reader-adapter mechanics
// (Probe offline, chunk commit, bounds, size guard, budget charging) so the
// only remaining work for the fork is the Downloader production implementation.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// ---- test doubles -------------------------------------------------------

type fakeDownloader struct {
	body       []byte
	readerBody []byte // optional: bytes the reader yields; 0 => body
	size       int64  // override; 0 => len(body)
	openErr    error
	opened     int
	closed     int
	lastRes    *core.Resource
}

func (d *fakeDownloader) Open(_ context.Context, r *core.Resource) (io.ReadSeekCloser, int64, error) {
	d.opened++
	d.lastRes = r
	if d.openErr != nil {
		return nil, 0, d.openErr
	}
	size := d.size
	if size == 0 {
		size = int64(len(d.body))
	}
	readerBytes := d.body
	if d.readerBody != nil {
		readerBytes = d.readerBody
	}
	return &seekNopCloser{Reader: bytes.NewReader(readerBytes), closed: &d.closed}, size, nil
}

type seekNopCloser struct {
	*bytes.Reader
	closed *int
}

func (s *seekNopCloser) Close() error { *s.closed++; return nil }

type testChunkWriter struct {
	chunks   []core.Chunk
	written  map[int64][]byte
	released []core.Chunk
}

func newTestChunkWriter(size, chunkSize int64) *testChunkWriter {
	var chunks []core.Chunk
	for start, idx := int64(0), 0; start < size; start, idx = start+chunkSize, idx+1 {
		n := chunkSize
		if size-start < chunkSize {
			n = size - start
		}
		chunks = append(chunks, core.Chunk{Index: idx, Start: start, Length: n, State: core.ChunkPending})
	}
	return &testChunkWriter{chunks: chunks, written: map[int64][]byte{}}
}

func (w *testChunkWriter) Next(context.Context) (core.Chunk, bool, error) {
	for i := range w.chunks {
		if w.chunks[i].State == core.ChunkPending {
			w.chunks[i].State = core.ChunkAssigned
			return w.chunks[i], true, nil
		}
	}
	return core.Chunk{}, false, nil
}
func (w *testChunkWriter) WriteChunk(_ context.Context, c core.Chunk, p []byte) error {
	w.written[int64(c.Index)] = append([]byte(nil), p...)
	for i := range w.chunks {
		if w.chunks[i].Index == c.Index {
			w.chunks[i].State = core.ChunkDone
		}
	}
	return nil
}
func (w *testChunkWriter) Verified(context.Context, core.Chunk, core.Hash) error { return nil }
func (w *testChunkWriter) Release(_ context.Context, c core.Chunk, _ error) error {
	w.released = append(w.released, c)
	return nil
}
func (w *testChunkWriter) concat() []byte {
	var out []byte
	for i := 0; i < len(w.chunks); i++ {
		out = append(out, w.written[int64(i)]...)
	}
	return out
}

type countingBudget struct{ bytes int }

func (b *countingBudget) Consume(_ context.Context, n int) error { b.bytes += n; return nil }

func ed2kResource(size int64) *core.Resource {
	return &core.Resource{
		ID: "res-test", Kind: core.KindED2K, Engine: core.EngineGo,
		Files:    []core.ResourceFile{{Index: 0, Path: "movie.bin", Size: size, Selected: true}},
		Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16), AICHRoot: strings.Repeat("cd", 20)},
	}
}

// ---- Probe / Supports ---------------------------------------------------

func TestSupportsOnlyED2K(t *testing.T) {
	tr := New(nil)
	if !tr.Supports(ed2kResource(10)) || tr.Supports(nil) {
		t.Fatal("Supports must claim only a non-nil KindED2K resource")
	}
	other := ed2kResource(10)
	other.Kind = core.KindTorrent
	if tr.Supports(other) {
		t.Fatal("Supports must not claim a torrent resource")
	}
}

func TestProbeOffline(t *testing.T) {
	tr := New(nil) // no downloader: Probe must still work
	got, err := tr.Probe(context.Background(), ed2kResource(4096))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got.Size != 4096 || !got.AcceptRanges || got.Filename != "movie.bin" {
		t.Fatalf("Probe result = %+v", got)
	}
}

func TestProbeRejectsMalformed(t *testing.T) {
	tr := New(nil)
	if _, err := tr.Probe(context.Background(), &core.Resource{Kind: core.KindED2K}); err == nil {
		t.Fatal("Probe must reject a resource with no file hash")
	}
	if _, err := tr.Probe(context.Background(), &core.Resource{
		Kind: core.KindED2K, Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16)},
	}); err == nil {
		t.Fatal("Probe must reject a resource with no files")
	}
}

// ---- Run ----------------------------------------------------------------

func TestRunWithoutDownloaderFailsLoudly(t *testing.T) {
	tr := New(nil)
	err := tr.Run(context.Background(), ed2kResource(10), newTestChunkWriter(10, 4), nil)
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("Run with no downloader must fail loudly, got %v", err)
	}
}

func TestRunCommitsAllChunks(t *testing.T) {
	body := []byte("0123456789abcdef") // 16 bytes
	dl := &fakeDownloader{body: body}
	tr := New(dl)
	w := newTestChunkWriter(int64(len(body)), 4)
	budget := &countingBudget{}
	if err := tr.Run(context.Background(), ed2kResource(int64(len(body))), w, budget); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := w.concat(); !bytes.Equal(got, body) {
		t.Fatalf("committed bytes = %q, want %q", got, body)
	}
	if budget.bytes != len(body) {
		t.Fatalf("budget charged %d, want %d", budget.bytes, len(body))
	}
	if dl.opened != 1 || dl.closed != 1 {
		t.Fatalf("downloader opened=%d closed=%d, want 1/1", dl.opened, dl.closed)
	}
}

func TestRunSizeMismatch(t *testing.T) {
	// The engine returns a well-formed (body, size) pair, but it disagrees
	// with the size frozen in the link Resource — that is the guard under test.
	body := bytes.Repeat([]byte("x"), 8)
	dl := &fakeDownloader{body: body, size: 8}
	tr := New(dl)
	w := newTestChunkWriter(8, 4)
	err := tr.Run(context.Background(), ed2kResource(999), w, nil)
	if err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("size mismatch must fail, got %v", err)
	}
	if dl.closed != 1 {
		t.Fatalf("reader must be closed on error, closed=%d", dl.closed)
	}
}

// progressFakeDownloader is the progress-aware variant of fakeDownloader. The
// transport uses it whenever the downloader offers the hook, but must pass a nil
// report when the writer cannot consume progress.
type progressFakeDownloader struct {
	*fakeDownloader
	progressed int
	gotReport  bool
}

func (d *progressFakeDownloader) OpenWithProgress(ctx context.Context, r *core.Resource,
	report func(done int64)) (io.ReadSeekCloser, int64, error) {
	d.progressed++
	d.gotReport = report != nil
	if report != nil {
		report(d.size / 2)
	}
	return d.fakeDownloader.Open(ctx, r)
}

// progressTestWriter adds the optional sink the scheduler's chunkWriter exposes.
type progressTestWriter struct {
	*testChunkWriter
	reports []int64
}

func (w *progressTestWriter) ReportProgress(done int64) { w.reports = append(w.reports, done) }

// Run must route engine bytes to the writer when it supports ProgressSink, and
// keep the plain Open contract when it does not (no behavioural change for
// non-eD2k writers).
func TestRunForwardsEngineProgressWhenWriterSupportsIt(t *testing.T) {
	body := []byte("0123456789abcdef")
	dl := &progressFakeDownloader{fakeDownloader: &fakeDownloader{body: body, size: 16}}
	tr := New(dl)

	w := &progressTestWriter{testChunkWriter: newTestChunkWriter(16, 4)}
	if err := tr.Run(context.Background(), ed2kResource(16), w, &countingBudget{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if dl.progressed != 1 || !dl.gotReport {
		t.Fatalf("progress-aware Open not used (calls=%d report=%v)", dl.progressed, dl.gotReport)
	}
	if len(w.reports) != 1 || w.reports[0] != 8 {
		t.Fatalf("engine progress not forwarded: %v", w.reports)
	}
	if got := w.concat(); !bytes.Equal(got, body) {
		t.Fatalf("committed bytes = %q, want %q", got, body)
	}

	// Writer without the sink: no bytes are reported and the transfer still
	// completes exactly as before.
	dl2 := &progressFakeDownloader{fakeDownloader: &fakeDownloader{body: body, size: 16}}
	plain := newTestChunkWriter(16, 4)
	if err := New(dl2).Run(context.Background(), ed2kResource(16), plain, &countingBudget{}); err != nil {
		t.Fatalf("Run (plain writer): %v", err)
	}
	if dl2.gotReport {
		t.Fatal("plain writer must not receive a progress callback")
	}
	if !bytes.Equal(plain.concat(), body) || dl2.opened != 1 {
		t.Fatalf("plain writer transfer changed (opened=%d)", dl2.opened)
	}
}

func TestRunOpenError(t *testing.T) {
	dl := &fakeDownloader{openErr: errors.New("kad timeout")}
	tr := New(dl)
	err := tr.Run(context.Background(), ed2kResource(10), newTestChunkWriter(10, 4), nil)
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("open error must surface, got %v", err)
	}
}

func TestRunReleasesOutOfBoundsChunk(t *testing.T) {
	body := []byte("0123456789")
	tr := New(&fakeDownloader{body: body})
	w := newTestChunkWriter(int64(len(body)), 4)
	// Inject one out-of-bounds chunk at the head.
	w.chunks = append([]core.Chunk{{Index: 99, Start: 8, Length: 8, State: core.ChunkPending}}, w.chunks...)
	err := tr.Run(context.Background(), ed2kResource(int64(len(body))), w, nil)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("out-of-bounds chunk must fail loudly, got %v", err)
	}
	if len(w.released) != 1 || w.released[0].Index != 99 {
		t.Fatalf("out-of-bounds chunk must be released, got %+v", w.released)
	}
}

func TestRunRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr := New(&fakeDownloader{body: []byte("0123456789")})
	err := tr.Run(ctx, ed2kResource(10), newTestChunkWriter(10, 4), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context must abort, got %v", err)
	}
}

// ---- extended branches (coverage) --------------------------------------

func TestNameStringAndAliases(t *testing.T) {
	tr := New(nil)
	if tr.Name() != "ed2k" {
		t.Fatalf("Name()=%q", tr.Name())
	}
	if !strings.Contains(tr.String(), "downloader=false") {
		t.Fatalf("String() with no downloader = %q", tr.String())
	}
	d := &fakeDownloader{body: []byte("x")}
	withDL := NewWithDownloader(d)
	if !strings.Contains(withDL.String(), "downloader=true") {
		t.Fatalf("String() with downloader = %q", withDL.String())
	}
	if withDL.Name() != "ed2k" {
		t.Fatal("NewWithDownloader must build an ed2k transport")
	}
}

func TestProbeRejectsNonED2K(t *testing.T) {
	_, err := New(nil).Probe(context.Background(), &core.Resource{Kind: core.KindTorrent})
	if err == nil {
		t.Fatal("Probe must reject a non-ed2k resource")
	}
}

func TestRunRejectsNonED2K(t *testing.T) {
	err := New(nil).Run(context.Background(), &core.Resource{Kind: core.KindSingle},
		newTestChunkWriter(1, 1), nil)
	if err == nil {
		t.Fatal("Run must reject a non-ed2k resource")
	}
}

func TestPrimaryFileFallsBackToFirst(t *testing.T) {
	r := ed2kResource(7)
	r.Files[0].Selected = false
	size, name, err := primaryFile(r)
	if err != nil || size != 7 || name != "movie.bin" {
		t.Fatalf("primaryFile fallback = %d %q %v", size, name, err)
	}
}

type errBudget struct{ err error }

func (b errBudget) Consume(context.Context, int) error { return b.err }

type failingWriter struct{ base *testChunkWriter }

func (w *failingWriter) Next(ctx context.Context) (core.Chunk, bool, error) {
	return w.base.Next(ctx)
}
func (w *failingWriter) WriteChunk(context.Context, core.Chunk, []byte) error {
	return errors.New("write failed")
}
func (w *failingWriter) Verified(context.Context, core.Chunk, core.Hash) error { return nil }
func (w *failingWriter) Release(context.Context, core.Chunk, error) error      { return nil }

func TestRunWriteChunkError(t *testing.T) {
	body := []byte("0123456789")
	w := &failingWriter{base: newTestChunkWriter(int64(len(body)), 4)}
	err := New(&fakeDownloader{body: body}).Run(context.Background(),
		ed2kResource(int64(len(body))), w, nil)
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("WriteChunk error must surface, got %v", err)
	}
}

func TestRunBudgetRejected(t *testing.T) {
	body := []byte("0123456789")
	budget := &errBudget{err: errors.New("rate limited")}
	err := New(&fakeDownloader{body: body}).Run(context.Background(),
		ed2kResource(int64(len(body))), newTestChunkWriter(int64(len(body)), 4), budget)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("budget rejection must surface, got %v", err)
	}
}

func TestRunTruncatedRead(t *testing.T) {
	// Engine reports the correct size, but its reader yields fewer bytes —
	// the truncation guard (not the size guard) must fire.
	dl := &fakeDownloader{body: bytes.Repeat([]byte("x"), 10), readerBody: []byte("short")}
	tr := New(dl)
	w := newTestChunkWriter(10, 4)
	err := tr.Run(context.Background(), ed2kResource(10), w, nil)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated read must fail, got %v", err)
	}
	if len(w.released) == 0 {
		t.Fatal("truncated chunk must be released")
	}
}

type seekFailReader struct{ *bytes.Reader }

func (s *seekFailReader) Seek(int64, int) (int64, error) { return 0, errors.New("seek failed") }
func (s *seekFailReader) Close() error                   { return nil }

type seekFailDownloader struct{}

func (seekFailDownloader) Open(context.Context, *core.Resource) (io.ReadSeekCloser, int64, error) {
	return &seekFailReader{Reader: bytes.NewReader([]byte("0123456789"))}, 10, nil
}

func TestRunSeekError(t *testing.T) {
	w := newTestChunkWriter(10, 4)
	err := New(seekFailDownloader{}).Run(context.Background(), ed2kResource(10), w, nil)
	if err == nil || !strings.Contains(err.Error(), "seek failed") {
		t.Fatalf("seek error must surface, got %v", err)
	}
	if len(w.released) == 0 {
		t.Fatal("chunk with failed seek must be released")
	}
}
