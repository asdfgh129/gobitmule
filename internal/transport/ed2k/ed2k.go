// Package ed2ktransport is the WBS 16 / ADR-0030 eD2k Transport skeleton.
//
// It implements the core.Transport contract for KindED2K Resources produced by
// resolve/ed2k.go. The design follows the source-spike in docs/25: goed2k is a
// task-type engine (fixed 9.28MiB eD2k pieces, self-managed *os.File, own hash
// and state) that structurally conflicts with the scheduler's ChunkWriter
// contract. The low-cost path is the SAME reader adapter BitTorrent already
// uses: let the engine download into a controlled temp directory, then read
// the completed file back through a seekable reader and feed ud's chunk plan.
//
// HONEST BOUNDARY (docs/25, ADR-0030):
//   - Probe is fully OFFLINE: size/name come from the frozen Resource/Identity
//     (ADR-0013), no network I/O, no new field.
//   - Run needs a Downloader seam. The PRODUCTION implementation (forked
//     goed2k, AddTransfer+Wait+ReadRange) is NOT wired yet; without an injected
//     Downloader, Run fails loudly with E_CAPABILITY_UNAVAILABLE instead of a
//     silent half-download. Real eD2k interop (ED-01/02/05/06) is NOT_EXECUTED.
//   - Consequently this transport is NOT registered by default in the daemon.
//     When the fork lands, a single transports.Register(ed2ktransport.New(...))
//     turns it on; registration changes no other kind (Supports claims only
//     KindED2K).
package ed2ktransport

import (
	"context"
	"fmt"
	"io"

	"github.com/asdfgh129/ud/internal/core"
)

// Downloader is the eD2k engine seam. The production implementation is the
// forked goed2k adapter (docs/25 §3): it adds the transfer with a controlled
// disk.FileHandler pointing at a temp dir, waits for IsFinished, and returns a
// seekable reader over the completed file. Tests inject a fake (docs/12 §1:
// CI never touches the real eD2k network).
type Downloader interface {
	// Open drives the transfer to completion and returns a reader positioned
	// at byte 0 over the WHOLE file, plus its total size. The caller owns the
	// reader and must Close it.
	Open(ctx context.Context, r *core.Resource) (io.ReadSeekCloser, int64, error)
}

// Transport is the eD2k implementation of core.Transport.
type Transport struct {
	downloader Downloader
}

// New builds the skeleton. With no Downloader, Probe still works offline and
// Run returns an explicit capability error — never a silent no-op.
func New(downloader Downloader) *Transport {
	return &Transport{downloader: downloader}
}

// NewWithDownloader is the seam constructor used by tests and by the future
// fork registration.
func NewWithDownloader(d Downloader) *Transport { return New(d) }

func (t *Transport) Name() string { return "ed2k" }

func (t *Transport) Supports(r *core.Resource) bool {
	return r != nil && r.Kind == core.KindED2K
}

// Probe learns the file size/name from the link-derived Resource. It performs
// NO network I/O: an eD2k link already carries the MD4 hash, size and name
// (docs/25 §2). A malformed Resource is refused rather than guessed.
func (t *Transport) Probe(_ context.Context, r *core.Resource) (*core.ProbeResult, error) {
	if !t.Supports(r) {
		return nil, core.Err(core.E_RESOLVE_FAILED, "ed2k transport requires an ed2k resource")
	}
	if len(r.Identity.ED2KFileHash) != 32 {
		return nil, core.Err(core.E_INVALID_INPUT, "ed2k resource has no valid MD4 file hash")
	}
	size, name, err := primaryFile(r)
	if err != nil {
		return nil, err
	}
	return &core.ProbeResult{Size: size, AcceptRanges: true, Filename: name}, nil
}

// Run drains the scheduler-owned chunk plan, reading the completed eD2k file
// through the Downloader's seekable reader and charging every byte to the
// shared Budget (BT reader pattern, docs/25 §3). Chunk boundaries, hashing and
// state remain the scheduler's — this transport only moves bytes.
func (t *Transport) Run(ctx context.Context, r *core.Resource, w core.ChunkWriter, b core.Budget) error {
	if !t.Supports(r) {
		return core.Err(core.E_RESOLVE_FAILED, "ed2k transport requires an ed2k resource")
	}
	if t.downloader == nil {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"eD2k engine not wired: forked goed2k Downloader is required (ADR-0030, docs/25)")
	}
	wantSize, _, err := primaryFile(r)
	if err != nil {
		return err
	}
	reader, size, err := t.downloader.Open(ctx, r)
	if err != nil {
		return core.Err(core.E_TRANSIENT_NETWORK, "ed2k download failed: %v", err)
	}
	defer reader.Close()
	if size != wantSize {
		return core.Err(core.E_CHECKSUM_MISMATCH,
			"ed2k downloaded size %d != link size %d", size, wantSize)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, ok, err := w.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if c.Start < 0 || c.Start+c.Length > size {
			_ = w.Release(ctx, c, core.Err(core.E_INVALID_INPUT,
				"chunk [%d,%d) outside file bounds [0,%d)", c.Start, c.Start+c.Length, size))
			return core.Err(core.E_INVALID_INPUT, "chunk range outside the eD2k file")
		}
		if _, err := reader.Seek(c.Start, io.SeekStart); err != nil {
			_ = w.Release(ctx, c, err)
			return core.Err(core.E_TRANSIENT_NETWORK, "ed2k seek failed at %d: %v", c.Start, err)
		}
		buf := make([]byte, c.Length)
		if err := readWithBudget(ctx, reader, buf, b); err != nil {
			_ = w.Release(ctx, c, err)
			return err
		}
		if err := w.WriteChunk(ctx, c, buf); err != nil {
			return err
		}
	}
}

// primaryFile returns the size and path of the selected (or first) file.
func primaryFile(r *core.Resource) (int64, string, error) {
	if len(r.Files) == 0 {
		return 0, "", core.Err(core.E_INVALID_INPUT, "ed2k resource has no files")
	}
	for _, f := range r.Files {
		if f.Selected {
			return f.Size, f.Path, nil
		}
	}
	return r.Files[0].Size, r.Files[0].Path, nil
}

// readWithBudget fills dst, charging every byte to the single global Limiter.
// Mirrors bttransport.readWithBudget so the rate authority stays unique.
func readWithBudget(ctx context.Context, src io.Reader, dst []byte, b core.Budget) error {
	const block = 32 << 10
	for off := 0; off < len(dst); {
		n := len(dst) - off
		if n > block {
			n = block
		}
		if b != nil {
			if err := b.Consume(ctx, n); err != nil {
				return err
			}
		}
		got, err := io.ReadFull(src, dst[off:off+n])
		off += got
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return core.Err(core.E_TRANSIENT_NETWORK,
					"ed2k read truncated at %d/%d bytes", off, len(dst))
			}
			return core.Err(core.E_TRANSIENT_NETWORK, "ed2k read error: %v", err)
		}
	}
	return nil
}

// String is a diagnostic aid; never used for dispatch.
func (t *Transport) String() string {
	return fmt.Sprintf("ed2k(downloader=%t)", t.downloader != nil)
}
