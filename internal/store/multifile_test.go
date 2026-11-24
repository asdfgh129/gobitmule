package store

// Store-level tests for the ADR-0027/0028 multi-file primitives the
// scheduler drives: file-scoped chunk claiming, per-file verification and
// atomic file-plan replacement.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
)

func multiFileFixture(t *testing.T) (*Store, core.TaskID, string, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "state.db"), core.NewEventBus(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id := core.NewID()
	now := time.Now().UnixMilli()
	if err := st.InsertTask(ctx, &core.Task{
		ID: id, State: core.StateRunning, Input: "multi://test",
		InputKind: core.InputURL, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	res := &core.Resource{ID: "res-" + string(id), Kind: core.KindSingle,
		Files: []core.ResourceFile{
			{Index: 0, Path: "video.mp4", Size: 4096, Selected: true, URL: "https://a/v"},
			{Index: 1, Path: "audio.m4a", Size: 2048, Selected: true, URL: "https://a/a"},
		}}
	if err := st.SaveResource(ctx, id, res); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChunks(ctx, id, 0, 4096, 2048); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChunks(ctx, id, 1, 2048, 2048); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "video.mp4.part")
	audio := filepath.Join(dir, "audio.m4a.part")
	if err := os.WriteFile(video, []byte(strings.Repeat("V", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte(strings.Repeat("A", 2048)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOption(ctx, id, "temp_path", video); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOption(ctx, id, "temp_path:1", audio); err != nil {
		t.Fatal(err)
	}
	return st, id, video, audio
}

func TestNextForFileScopesToOwnFile(t *testing.T) {
	st, id, _, _ := multiFileFixture(t)
	ctx := context.Background()
	// File 0's writer must never see file 1's chunks (and vice versa).
	c0, ok, err := st.NextForFile(ctx, id, 0)
	if err != nil || !ok {
		t.Fatalf("NextForFile(0): ok=%v err=%v", ok, err)
	}
	if c0.FileIndex != 0 || c0.Length != 2048 {
		t.Fatalf("chunk = %+v", c0)
	}
	c0b, ok, err := st.NextForFile(ctx, id, 0)
	if err != nil || !ok {
		t.Fatalf("NextForFile(0) second: ok=%v err=%v", ok, err)
	}
	if c0b.FileIndex != 0 {
		t.Fatalf("second chunk = %+v", c0b)
	}
	if _, ok, _ := st.NextForFile(ctx, id, 0); ok {
		t.Fatal("file 0 has only two chunks")
	}
	c1, ok, err := st.NextForFile(ctx, id, 1)
	if err != nil || !ok {
		t.Fatalf("NextForFile(1): ok=%v err=%v", ok, err)
	}
	if c1.FileIndex != 1 || c1.Length != 2048 {
		t.Fatalf("chunk = %+v", c1)
	}
	if _, ok, _ := st.NextForFile(ctx, id, 1); ok {
		t.Fatal("file 1 has only one chunk")
	}
}

func TestVerifyFileChunksScopesAndDigests(t *testing.T) {
	st, id, video, audio := multiFileFixture(t)
	ctx := context.Background()
	sumV := sha256.Sum256([]byte(strings.Repeat("V", 2048)))
	sum := hex.EncodeToString(sumV[:])
	sumA := sha256.Sum256([]byte(strings.Repeat("A", 2048)))
	// File 0 has TWO chunks (4096/2048); both must be done for a clean verify.
	for _, c := range []core.Chunk{mustNext(t, st, id, 0), mustNext(t, st, id, 0)} {
		if err := st.WriteChunkDone(ctx, c, "res-"+string(id), sum, 0); err != nil {
			t.Fatal(err)
		}
	}
	c1 := mustNext(t, st, id, 1)
	if err := st.WriteChunkDone(ctx, c1, "res-"+string(id), hex.EncodeToString(sumA[:]), 0); err != nil {
		t.Fatal(err)
	}

	// Both files' done chunks verify against their own .part bytes.
	bad0, err := st.VerifyFileChunks(ctx, id, "res-"+string(id), 0, video)
	if err != nil || len(bad0) != 0 {
		t.Fatalf("file 0 verify: bad=%v err=%v", bad0, err)
	}
	bad1, err := st.VerifyFileChunks(ctx, id, "res-"+string(id), 1, audio)
	if err != nil || len(bad1) != 0 {
		t.Fatalf("file 1 verify: bad=%v err=%v", bad1, err)
	}

	// Corrupt file 1: its chunk goes bad while file 0 stays clean.
	if err := os.WriteFile(audio, []byte(strings.Repeat("X", 2048)), 0o600); err != nil {
		t.Fatal(err)
	}
	bad1, err = st.VerifyFileChunks(ctx, id, "res-"+string(id), 1, audio)
	if err != nil || len(bad1) != 1 {
		t.Fatalf("corrupted file 1: bad=%v err=%v", bad1, err)
	}
	bad0, err = st.VerifyFileChunks(ctx, id, "res-"+string(id), 0, video)
	if err != nil || len(bad0) != 0 {
		t.Fatalf("file 0 after file 1 corruption: bad=%v err=%v", bad0, err)
	}

	// A missing .part surfaces the open error alongside every done chunk of
	// that file flagged bad — the scheduler fails the run and the Recover
	// path resets the chunks on the next start (same contract as
	// VerifyAllChunks for the single-file case).
	missing := filepath.Join(filepath.Dir(audio), "missing.part")
	bad1, err = st.VerifyFileChunks(ctx, id, "res-"+string(id), 1, missing)
	if err == nil || len(bad1) != 1 {
		t.Fatalf("missing file 1: bad=%v err=%v", bad1, err)
	}
}

func TestReplaceResourceFilesPersistsURLs(t *testing.T) {
	st, id, _, _ := multiFileFixture(t)
	ctx := context.Background()
	plan := []core.ResourceFile{
		{Index: 0, Path: "master.mp4", Size: 999, Selected: true, URL: "https://a/plan-v.m3u8"},
		{Index: 1, Path: "master.audio.m3u8", Size: 111, Selected: true, URL: "https://a/plan-a.m3u8"},
	}
	if err := st.ReplaceResourceFiles(ctx, "res-"+string(id), plan); err != nil {
		t.Fatal(err)
	}
	res, err := st.GetResource(ctx, "res-"+string(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 2 || res.Files[0].URL != "https://a/plan-v.m3u8" ||
		res.Files[1].URL != "https://a/plan-a.m3u8" {
		t.Fatalf("files = %+v", res.Files)
	}
}

func mustNext(t *testing.T, st *Store, id core.TaskID, fileIdx int) core.Chunk {
	t.Helper()
	c, ok, err := st.NextForFile(context.Background(), id, fileIdx)
	if err != nil || !ok {
		t.Fatalf("NextForFile(%d): ok=%v err=%v", fileIdx, ok, err)
	}
	return c
}
