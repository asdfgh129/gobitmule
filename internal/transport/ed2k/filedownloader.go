package ed2ktransport

import (
	"context"
	"io"
	"os"

	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/security"
)

// LocalFileDownloader is a concrete Downloader that reads an eD2k file which a
// real eD2k client (e.g. eMule on lab host 209) has ALREADY completed into a
// controlled directory (docs/25 §3 "reader adapter" pattern, same as BT).
//
// Boundary — read this before using:
//   - It does NOT download anything. The engine/peer is assumed to have placed
//     the COMPLETE file under root. It only opens a seekable reader over it.
//   - It is therefore suitable for interop/validation and for the "external
//     engine owns download, ud owns chunk plan/hash/budget/state" topology —
//     NOT a replacement for the forked goed2k AddTransfer+Wait downloader.
//   - The file name comes from an untrusted ed2k:// link, so it is confined by
//     security.PathGuard: traversal, absolute paths and reserved names refused.
//   - The on-disk size is reported to Run, which compares it against the size
//     frozen in the Resource and fails loudly on mismatch.
//
// Lab use (2026-11-22): 209 eMule Kad is live (TCP 31969 / UDP 41296) with
// completed files under D:\eMule\incoming; this adapter lets ud's scheduler
// drain those bytes through the same Budget/ChunkWriter path as every other
// transport, the last offline-verifiable seam before wiring the fork.
type LocalFileDownloader struct {
	root  string
	guard security.PathGuard
}

// NewLocalFileDownloader binds a controlled directory of completed eD2k files.
// An empty root is rejected at Open time (not here) so callers can build the
// value before config is loaded.
func NewLocalFileDownloader(root string) *LocalFileDownloader {
	return &LocalFileDownloader{root: root}
}

// Open resolves the selected (or first) file name under root and returns a
// seekable reader positioned at byte 0 plus the on-disk size. Path safety is
// delegated to security.PathGuard (docs/11 §4).
func (d *LocalFileDownloader) Open(_ context.Context, r *core.Resource) (io.ReadSeekCloser, int64, error) {
	if d == nil || d.root == "" {
		return nil, 0, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"local eD2k file directory is not configured")
	}
	_, name, err := primaryFile(r)
	if err != nil {
		return nil, 0, err
	}
	path, err := d.guard.Clean(d.root, name)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, core.Err(core.E_TRANSIENT_NETWORK,
			"open completed eD2k file %q failed: %v", name, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "stat eD2k file %q failed: %v", name, err)
	}
	if st.IsDir() {
		_ = f.Close()
		return nil, 0, core.Err(core.E_INVALID_INPUT, "eD2k path %q is a directory", name)
	}
	return f, st.Size(), nil
}

var _ Downloader = (*LocalFileDownloader)(nil)
