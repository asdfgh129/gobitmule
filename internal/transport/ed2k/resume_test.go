package ed2ktransport

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// statefulFakeEngine records the resume wiring the adapter is supposed to do.
// A plain fakeEngine deliberately does not implement statefulEngine, which is
// how the adapter detects "this engine cannot resume" without requiring every
// test double to grow the methods.
type statefulFakeEngine struct {
	*fakeEngine
	statePath  string
	loadCalls  int
	loadErr    error
	loadedPath string
}

func (e *statefulFakeEngine) SetStatePath(path string) { e.statePath = path }
func (e *statefulFakeEngine) LoadState(path string) error {
	e.loadCalls++
	e.loadedPath = path
	return e.loadErr
}

func resumeResource(hash string) *core.Resource {
	return &core.Resource{
		Kind:     core.KindED2K,
		Files:    []core.ResourceFile{{Path: "resume.bin", Size: 16, Selected: true}},
		Identity: core.ProtocolIdentity{ED2KFileHash: hash},
	}
}

// A configured resume dir must point the engine at a per-hash state file and
// load it BEFORE AddLink, so a restart continues from the pieces already on
// disk instead of restarting the transfer at byte 0.
func TestOpenLoadsResumeStateBeforeAddLink(t *testing.T) {
	h, _ := completedHandle(t, []byte("0123456789abcdef"))
	eng := &statefulFakeEngine{fakeEngine: &fakeEngine{handle: h}}
	stateDir := t.TempDir()
	hash := strings.Repeat("AB", 16)

	dl := newGoed2kDownloaderWithFactory(
		Goed2kConfig{OutputDir: t.TempDir(), StateDir: stateDir},
		func() (engine, error) { return eng, nil },
	)
	reader, _, err := dl.Open(context.Background(), resumeResource(hash))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = reader.Close() // Windows: TempDir cleanup needs the handle released
	if eng.loadCalls != 1 {
		t.Fatalf("LoadState calls = %d, want 1", eng.loadCalls)
	}
	want := filepath.Join(stateDir, strings.ToLower(hash)+".json")
	if eng.statePath != want {
		t.Fatalf("state path = %q, want %q", eng.statePath, want)
	}
	if eng.loadedPath != "" {
		// Empty means "use the path already configured"; passing an explicit
		// path would bypass SetStatePath and re-create the store.
		t.Fatalf("LoadState should use the configured path, got %q", eng.loadedPath)
	}
	if eng.addedLink == "" {
		t.Fatal("AddLink was not reached after resume load")
	}
}

// Without a resume dir the adapter must not touch the engine state at all: that
// is the ADR-0002 baseline where ud is the only state authority.
func TestOpenWithoutStateDirSkipsResume(t *testing.T) {
	h, _ := completedHandle(t, []byte("0123456789abcdef"))
	eng := &statefulFakeEngine{fakeEngine: &fakeEngine{handle: h}}
	dl := newGoed2kDownloaderWithFactory(
		Goed2kConfig{OutputDir: t.TempDir()},
		func() (engine, error) { return eng, nil },
	)
	reader, _, err := dl.Open(context.Background(), resumeResource(strings.Repeat("cd", 16)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = reader.Close()
	if eng.loadCalls != 0 || eng.statePath != "" {
		t.Fatalf("state touched without a resume dir (calls=%d path=%q)", eng.loadCalls, eng.statePath)
	}
}

// A corrupt or missing resume file must degrade to a from-scratch transfer, not
// fail the task.
func TestOpenToleratesResumeLoadFailure(t *testing.T) {
	h, _ := completedHandle(t, []byte("0123456789abcdef"))
	eng := &statefulFakeEngine{
		fakeEngine: &fakeEngine{handle: h},
		loadErr:    errors.New("state file is corrupt"),
	}
	dl := newGoed2kDownloaderWithFactory(
		Goed2kConfig{OutputDir: t.TempDir(), StateDir: t.TempDir()},
		func() (engine, error) { return eng, nil },
	)
	reader, _, err := dl.Open(context.Background(), resumeResource(strings.Repeat("ef", 16)))
	if err != nil {
		t.Fatalf("resume load failure must not fail the transfer: %v", err)
	}
	_ = reader.Close()
	if eng.loadCalls != 1 || eng.addedLink == "" {
		t.Fatalf("transfer did not proceed after resume failure (calls=%d link=%q)", eng.loadCalls, eng.addedLink)
	}
}
