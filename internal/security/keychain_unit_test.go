package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

func TestFileOrMemoryDegradation(t *testing.T) {
	be, err := fileOrMemory(CredentialStoreOptions{})
	if err != nil {
		t.Fatalf("no dir: %v", err)
	}
	if be.kind() != BackendMemory {
		t.Fatalf("kind = %q, want memory", be.kind())
	}
	be, err = fileOrMemory(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("with dir: %v", err)
	}
	if be.kind() != BackendFile {
		t.Fatalf("kind = %q, want file", be.kind())
	}
}

func TestFileBackendKindAndScheme(t *testing.T) {
	be, err := newFileBackend(t.TempDir())
	if err != nil {
		t.Fatalf("newFileBackend: %v", err)
	}
	if be.kind() != BackendFile || be.scheme() != schemeFile {
		t.Fatalf("kind/scheme = %q/%q", be.kind(), be.scheme())
	}
}

// The directory creation failure is a real configuration error and must stop
// startup, not silently fall back to something with weaker guarantees.
func TestFileBackendMkdirFailure(t *testing.T) {
	dir := t.TempDir()
	// A regular file occupies the path where the credential directory must live.
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if _, err := newFileBackend(dir); err == nil {
		t.Fatal("creating the credential dir over a regular file must fail")
	}
}

func TestFileBackendDeleteMissingIsNoop(t *testing.T) {
	be, err := newFileBackend(t.TempDir())
	if err != nil {
		t.Fatalf("newFileBackend: %v", err)
	}
	id, err := newCredentialID()
	if err != nil {
		t.Fatalf("newCredentialID: %v", err)
	}
	// be.delete sees the raw id: the store-level idempotency test cannot reach
	// this branch because a foreign ref never gets past the prefix check.
	if err := be.delete(context.Background(), id); err != nil {
		t.Fatalf("delete missing = %v, want nil", err)
	}
}

// File writes must fail loudly when the directory is not writable: a silent
// fall-back would lose credentials the user believes are persisted. Root (and
// capability holders) bypass permission checks, so on such hosts this scenario
// cannot be exercised and is skipped rather than falsely reported.
func TestFileBackendPutFailsWhenDirUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks: NOT_EXECUTED on this host")
	}
	if runtime.GOOS == "windows" {
		// Windows cannot express "unwritable to the owner" through Go's
		// os.Chmod (it toggles only the read-only attribute), so the scenario
		// is NOT_EXECUTED here rather than falsely reported.
		t.Skip("os.Chmod cannot make a directory unwritable on Windows: NOT_EXECUTED on this host")
	}
	dir := t.TempDir()
	be, err := newFileBackend(dir)
	if err != nil {
		t.Fatalf("newFileBackend: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "credentials"), 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "credentials"), 0o700) })
	id, err := newCredentialID()
	if err != nil {
		t.Fatalf("newCredentialID: %v", err)
	}
	err = be.put(context.Background(), id, "s3cret")
	if err == nil {
		t.Fatal("write into a read-only dir must fail")
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.E_INTERNAL {
		t.Fatalf("err = %v, want E_INTERNAL", err)
	}
	// No half-written artifact may survive the failure.
	entries, _ := os.ReadDir(filepath.Join(dir, "credentials"))
	if len(entries) != 0 {
		t.Fatalf("leftover entries after failed put: %v", entries)
	}
}

// A stored empty value is not a credential: Get must treat it as absent so a
// transport never proceeds with a blank password.
func TestGetEmptyStoredValueIsMissing(t *testing.T) {
	be := newMemoryBackend()
	id, err := newCredentialID()
	if err != nil {
		t.Fatalf("newCredentialID: %v", err)
	}
	if err := be.put(context.Background(), id, ""); err != nil {
		t.Fatalf("put empty: %v", err)
	}
	s := newKeychainStore(be)
	if _, err := s.Get(context.Background(), core.CredentialRef(schemeMemory+"://"+refHostPath+"/"+id)); err == nil {
		t.Fatal("empty stored value must surface as not found")
	}
}

func TestStorePutAppliesSecretValidation(t *testing.T) {
	s := NewMemoryCredentialStore()
	ctx := context.Background()
	if _, err := s.Put(ctx, ""); err == nil {
		t.Fatal("empty secret must be rejected at the store level")
	}
	if _, err := s.Put(ctx, "a\nb"); err == nil {
		t.Fatal("control characters must be rejected at the store level")
	}
	if _, err := s.Put(ctx, strings.Repeat("a", maxKeychainSecretBytes+1)); err == nil {
		t.Fatal("oversized secret must be rejected at the store level")
	}
}

// If the target name is taken by a directory, the atomic rename must fail
// loudly and clean up its temp file instead of leaving debris.
func TestFileBackendPutFailsWhenTargetIsDirectory(t *testing.T) {
	_, dir := newFileStore(t)
	// A valid-format id that collides with a directory.
	id := "00112233445566778899aabbccddeeff"
	if err := os.Mkdir(filepath.Join(dir, id), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	be, err := newFileBackend(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("newFileBackend: %v", err)
	}
	err = be.put(context.Background(), id, "s3cret")
	if err == nil {
		t.Fatal("publishing over a directory must fail")
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.E_INTERNAL {
		t.Fatalf("err = %v, want E_INTERNAL", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %v", entries)
		}
	}
}

// A credential path that exists but is not a regular file must fail loudly:
// stat succeeds, the mode guard passes (0700), and the read itself errors.
// Root cannot produce this scenario (it reads anything), so it is skipped
// rather than falsely reported there.
func TestFileBackendGetFailsWhenPathIsDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks: NOT_EXECUTED on this host")
	}
	s, dir := newFileStore(t)
	ctx := context.Background()
	ref, err := s.Put(ctx, "s3cret")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	id := strings.TrimPrefix(string(ref), "file://ud/cred/")
	if err := os.Remove(filepath.Join(dir, id)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, id), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := s.Get(ctx, ref); err == nil {
		t.Fatal("reading a directory must fail")
	}
}
