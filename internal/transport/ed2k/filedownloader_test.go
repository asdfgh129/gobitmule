package ed2ktransport

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

// Tests for LocalFileDownloader (docs/25 §3 reader-adapter pattern).
// A real on-disk file stands in for the eMule-completed file on lab host 209;
// no network I/O. They prove the byte path from completed file -> seekable
// reader -> chunk plan -> ChunkWriter is byte-exact and path-safe.

func writeCompleted(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func localResource(size int64, name string) *core.Resource {
	return &core.Resource{
		ID: "res-local", Kind: core.KindED2K, Engine: core.EngineGo,
		Files:    []core.ResourceFile{{Index: 0, Path: name, Size: size, Selected: true}},
		Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16)},
	}
}

func TestLocalDownloaderOpenAndSize(t *testing.T) {
	dir := t.TempDir()
	body := []byte("0123456789abcdef")
	writeCompleted(t, dir, "done.bin", body)

	dl := NewLocalFileDownloader(dir)
	r, size, err := dl.Open(context.Background(), localResource(int64(len(body)), "done.bin"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	if size != int64(len(body)) {
		t.Fatalf("size=%d want %d", size, len(body))
	}
	buf := make([]byte, len(body))
	if _, err := r.Read(buf); err != nil || !bytes.Equal(buf, body) {
		t.Fatalf("read = %q (%v), want %q", buf, err, body)
	}
}

func TestLocalDownloaderEndToEndBytes(t *testing.T) {
	// Byte-exact Run over a REAL file: this is the interop seam 209 provides.
	dir := t.TempDir()
	body := bytes.Repeat([]byte("eMule-Kad-31969-41296|"), 37) // 814 bytes
	writeCompleted(t, dir, "completed.bin", body)

	tr := New(NewLocalFileDownloader(dir))
	w := newTestChunkWriter(int64(len(body)), 64)
	budget := &countingBudget{}
	if err := tr.Run(context.Background(), localResource(int64(len(body)), "completed.bin"), w, budget); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := w.concat(); !bytes.Equal(got, body) {
		t.Fatalf("committed %d bytes != source %d bytes", len(got), len(body))
	}
	if budget.bytes != len(body) {
		t.Fatalf("budget=%d want %d", budget.bytes, len(body))
	}
}

func TestLocalDownloaderSizeGuardViaRun(t *testing.T) {
	dir := t.TempDir()
	body := []byte("short")
	writeCompleted(t, dir, "f.bin", body)

	// Resource freezes a larger size than the file on disk -> Run detects
	// the size mismatch from the downloader's reported size.
	tr := New(NewLocalFileDownloader(dir))
	w := newTestChunkWriter(999, 4)
	err := tr.Run(context.Background(), localResource(999, "f.bin"), w, nil)
	if err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("size mismatch must fail, got %v", err)
	}
}

func TestLocalDownloaderRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	dl := NewLocalFileDownloader(dir)
	for _, name := range []string{"../escape.bin", "a/../../b.bin", "/abs.bin", "C:\\win.bin"} {
		if _, _, err := dl.Open(context.Background(), localResource(10, name)); err == nil {
			t.Fatalf("path %q must be rejected", name)
		}
	}
}

func TestLocalDownloaderUnconfigured(t *testing.T) {
	for _, dl := range []*LocalFileDownloader{nil, NewLocalFileDownloader("")} {
		var d Downloader = dl
		_, _, err := d.Open(context.Background(), localResource(10, "x.bin"))
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("unconfigured downloader must fail loudly, got %v", err)
		}
	}
}

func TestLocalDownloaderMissingFile(t *testing.T) {
	dl := NewLocalFileDownloader(t.TempDir())
	_, _, err := dl.Open(context.Background(), localResource(10, "nope.bin"))
	if err == nil || !strings.Contains(err.Error(), "open completed") {
		t.Fatalf("missing file must surface, got %v", err)
	}
}

func TestLocalDownloaderRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	dl := NewLocalFileDownloader(dir)
	_, _, err := dl.Open(context.Background(), localResource(10, "subdir"))
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("directory must be rejected, got %v", err)
	}
}
