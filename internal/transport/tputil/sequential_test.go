package tputil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

type fakeChunkWriter struct {
	chunks   []core.Chunk
	cursor   int
	written  map[int][]byte
	released []int
	writeErr error
	onWrite  func()
}

func newFakeWriter(chunks ...core.Chunk) *fakeChunkWriter {
	return &fakeChunkWriter{chunks: chunks, written: map[int][]byte{}}
}

func (w *fakeChunkWriter) Next(context.Context) (core.Chunk, bool, error) {
	if w.cursor >= len(w.chunks) {
		return core.Chunk{}, false, nil
	}
	c := w.chunks[w.cursor]
	w.cursor++
	return c, true, nil
}

func (w *fakeChunkWriter) WriteChunk(_ context.Context, c core.Chunk, p []byte) error {
	if w.writeErr != nil {
		return w.writeErr
	}
	w.written[c.Index] = append([]byte(nil), p...)
	if w.onWrite != nil {
		w.onWrite()
	}
	return nil
}

func (w *fakeChunkWriter) Verified(context.Context, core.Chunk, core.Hash) error { return nil }
func (w *fakeChunkWriter) Release(_ context.Context, c core.Chunk, _ error) error {
	w.released = append(w.released, c.Index)
	return nil
}

type seqCountBudget struct{ total int }

func (b *seqCountBudget) Consume(_ context.Context, n int) error { b.total += n; return nil }

type seqFailBudget struct{ err error }

func (b seqFailBudget) Consume(context.Context, int) error { return b.err }

func chunk(index int, start, length int64) core.Chunk {
	return core.Chunk{Index: index, Start: start, Length: length}
}

func TestSequentialCommitAllChunks(t *testing.T) {
	w := newFakeWriter(chunk(0, 0, 4), chunk(1, 4, 4))
	first, ok, err := w.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("Next: %v %v", ok, err)
	}
	if err := SequentialCommit(context.Background(), bytes.NewReader([]byte("abcdefgh")), 8, 0, w, first); err != nil {
		t.Fatalf("SequentialCommit: %v", err)
	}
	if string(w.written[0]) != "abcd" || string(w.written[1]) != "efgh" {
		t.Fatalf("written = %q / %q", w.written[0], w.written[1])
	}
	if len(w.released) != 0 {
		t.Fatalf("unexpected releases: %v", w.released)
	}
}

func TestSequentialCommitResumeSkip(t *testing.T) {
	w := newFakeWriter(chunk(1, 4, 4))
	first, _, _ := w.Next(context.Background())
	if err := SequentialCommit(context.Background(), bytes.NewReader([]byte("abcdefgh")), 8, 0, w, first); err != nil {
		t.Fatalf("SequentialCommit: %v", err)
	}
	if string(w.written[1]) != "efgh" {
		t.Fatalf("resume wrote %q, want efgh", w.written[1])
	}
}

func TestSequentialCommitErrors(t *testing.T) {
	t.Run("chunk outside stream", func(t *testing.T) {
		w := newFakeWriter(chunk(0, 8, 4))
		first, _, _ := w.Next(context.Background())
		err := SequentialCommit(context.Background(), bytes.NewReader([]byte("abcdefgh")), 8, 0, w, first)
		if err == nil || len(w.released) != 1 {
			t.Fatalf("err = %v, released = %v", err, w.released)
		}
	})
	t.Run("short reader", func(t *testing.T) {
		w := newFakeWriter(chunk(0, 0, 8))
		first, _, _ := w.Next(context.Background())
		err := SequentialCommit(context.Background(), bytes.NewReader([]byte("abc")), 8, 0, w, first)
		if !errors.Is(err, io.ErrUnexpectedEOF) || len(w.released) != 1 {
			t.Fatalf("err = %v, released = %v", err, w.released)
		}
	})
	t.Run("skip short", func(t *testing.T) {
		w := newFakeWriter(chunk(1, 4, 4))
		first, _, _ := w.Next(context.Background())
		err := SequentialCommit(context.Background(), bytes.NewReader([]byte("ab")), 8, 0, w, first)
		if !errors.Is(err, io.EOF) || len(w.released) != 1 {
			t.Fatalf("err = %v, released = %v", err, w.released)
		}
	})
	t.Run("write failure", func(t *testing.T) {
		w := newFakeWriter(chunk(0, 0, 4))
		w.writeErr = errors.New("disk full")
		first, _, _ := w.Next(context.Background())
		if err := SequentialCommit(context.Background(), bytes.NewReader([]byte("abcd")), 4, 0, w, first); err == nil {
			t.Fatal("write failure must surface")
		}
	})
	t.Run("cancel after first chunk", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := newFakeWriter(chunk(0, 0, 4), chunk(1, 4, 4))
		w.onWrite = cancel
		first, _, _ := w.Next(ctx)
		if err := SequentialCommit(ctx, bytes.NewReader([]byte("abcdefgh")), 8, 0, w, first); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestChanReaderOrdersSourcesAndChargesBudget(t *testing.T) {
	budget := &seqCountBudget{}
	closed := 0
	src := []string{"ab", "cde", ""}
	i := 0
	r := NewChanReader(func() ([]byte, error, bool) {
		if i >= len(src) {
			return nil, nil, false
		}
		body := src[i]
		i++
		return []byte(body), nil, true
	}, func() { closed++ }, budget)
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "abcde" {
		t.Fatalf("read = %q, err = %v", got, err)
	}
	if budget.total != 5 {
		t.Fatalf("budget charged %d, want 5", budget.total)
	}
	r.Close()
	r.Close()
	if closed != 2 { // Close is delegated verbatim; callers may own idempotency.
		t.Fatalf("stop called %d times", closed)
	}
}

func TestChanReaderStickyErrorAndNilBudget(t *testing.T) {
	boom := errors.New("segment failed")
	calls := 0
	r := NewChanReader(func() ([]byte, error, bool) {
		calls++
		if calls == 1 {
			return []byte("ok"), nil, true
		}
		return nil, boom, true
	}, nil, nil)
	buf := make([]byte, 2)
	n, err := r.Read(buf)
	if n != 2 || err != nil || string(buf) != "ok" {
		t.Fatalf("first read = %d %q %v", n, buf, err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	if _, err := r.Read(buf); !errors.Is(err, boom) {
		t.Fatalf("sticky error = %v", err)
	}
}

func TestChanReaderBudgetFailure(t *testing.T) {
	boom := errors.New("rate limit canceled")
	r := NewChanReader(func() ([]byte, error, bool) {
		return []byte("payload"), nil, true
	}, nil, seqFailBudget{err: boom})
	if _, err := r.Read(make([]byte, 3)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Read(make([]byte, 3)); !errors.Is(err, boom) {
		t.Fatalf("sticky err = %v", err)
	}
}

func TestChanReaderEmptySourceIsEOF(t *testing.T) {
	r := NewChanReader(func() ([]byte, error, bool) { return nil, nil, false }, nil, nil)
	if n, err := r.Read(make([]byte, 4)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read = %d, %v", n, err)
	}
}
