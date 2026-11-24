package tputil

import (
	"context"
	"io"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// SequentialCommit drains an ordered byte stream into the scheduler-owned
// chunk plan (docs/04 §5: transports move bytes; the plan stays authoritative).
//
// `first` must already be claimed via w.Next(). Bytes before first.Start are
// skipped — crash recovery reuses verified progress, and those bytes still
// traverse the network, so THE READER must charge every byte (skipped or
// written) to the Budget: this helper deliberately never touches the rate
// authority itself, keeping scheduler.Limiter the single one (check_arch
// guard 4). Total is the whole stream size the chunk plan was built from;
// streamOffset is the task-stream byte offset where `r` BEGINS (the resume
// source starts at the segment containing first.Start, not at byte 0), so
// the in-reader skip is first.Start - streamOffset. A chunk that reaches
// past total is released and the run fails loudly instead of writing a
// short file.
//
// Users: bttransport copies from a seekable torrent reader; hlstransport /
// dashtransport feed an ordered segment stream. The loop is shared so the
// first-chunk / bounds / release semantics stay identical everywhere.
func SequentialCommit(ctx context.Context, r io.Reader, total, streamOffset int64, w core.ChunkWriter, first core.Chunk) error {
	if skip := first.Start - streamOffset; skip > 0 {
		if _, err := io.CopyN(io.Discard, r, skip); err != nil {
			_ = w.Release(ctx, first, err)
			return err
		}
	}
	cur := first
	for {
		if cur.Start+cur.Length > total {
			_ = w.Release(ctx, cur, core.Err(core.E_INVALID_INPUT,
				"chunk [%d,%d) outside stream size %d", cur.Start, cur.Start+cur.Length, total))
			return core.Err(core.E_INVALID_INPUT, "chunk [%d,%d) outside stream size %d",
				cur.Start, cur.Start+cur.Length, total)
		}
		buf := make([]byte, cur.Length)
		if _, err := io.ReadFull(r, buf); err != nil {
			_ = w.Release(ctx, cur, err)
			return err
		}
		if err := w.WriteChunk(ctx, cur, buf); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		next, ok, err := w.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		cur = next
	}
}

// ChanReader turns an ordered channel of byte slices (or errors) into one
// sequential io.Reader over the concatenated stream. Every byte is charged
// to the Budget here — including bytes the caller later skips — so rate
// limiting stays with the single authority. Close aborts the producing
// pipeline via the caller's stop function.
type ChanSource func() ([]byte, error, bool) // body, err, ok

type ChanReader struct {
	src    ChanSource
	close  func()
	cur    []byte
	err    error
	done   bool
	budget core.Budget
}

// NewChanReader wraps a pull-style source. The source returns ok=false for
// end of stream; a non-nil err is sticky and surfaced by every later Read.
func NewChanReader(src ChanSource, stop func(), b core.Budget) *ChanReader {
	return &ChanReader{src: src, close: stop, budget: b}
}

func (cr *ChanReader) Read(p []byte) (int, error) {
	if cr.err != nil {
		return 0, cr.err
	}
	for len(cr.cur) == 0 {
		if cr.done {
			return 0, io.EOF
		}
		body, err, ok := cr.src()
		if !ok {
			cr.done = true
			return 0, io.EOF
		}
		if err != nil {
			cr.err = err
			return 0, err
		}
		cr.cur = body
	}
	n := len(p)
	if n > len(cr.cur) {
		n = len(cr.cur)
	}
	if cr.budget != nil {
		if err := cr.budget.Consume(context.Background(), n); err != nil {
			cr.err = err
			return 0, err
		}
	}
	copy(p[:n], cr.cur[:n])
	cr.cur = cr.cur[n:]
	return n, nil
}

// Close aborts the producing pipeline. It is safe to call more than once.
func (cr *ChanReader) Close() {
	if cr.close != nil {
		cr.close()
	}
}
