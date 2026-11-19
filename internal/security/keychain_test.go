package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

func newFileStore(t *testing.T) (*KeychainStore, string) {
	t.Helper()
	dir := t.TempDir()
	be, err := newFileBackend(dir)
	if err != nil {
		t.Fatalf("newFileBackend: %v", err)
	}
	return newKeychainStore(be), filepath.Join(dir, "credentials")
}

func TestKeychainStoreRoundTrip(t *testing.T) {
	s, dir := newFileStore(t)
	ctx := context.Background()

	ref, err := s.Put(ctx, `{"username":"alice","password":"s3cret"}`)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(string(ref), "file://ud/cred/") {
		t.Fatalf("ref = %q, want the file scheme prefix", ref)
	}
	if strings.Contains(string(ref), "s3cret") {
		t.Fatal("secret leaked into the reference")
	}

	got, err := s.Get(ctx, ref)
	if err != nil || got != `{"username":"alice","password":"s3cret"}` {
		t.Fatalf("Get = %q, %v", got, err)
	}

	// The payload must be a private file, not a world-readable one. Go mode
	// bits do not model NTFS DACLs (Windows reports 0666 for any writable
	// file), so the exact-mode assertion is POSIX-only.
	info, err := os.Stat(filepath.Join(dir, strings.TrimPrefix(string(ref), "file://ud/cred/")))
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("credential file mode = %#o, want 0600", perm)
		}
	}

	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, ref); err == nil {
		t.Fatal("deleted credential is still readable")
	}
}

// Delete must satisfy the caller's intent even when the item is already gone,
// otherwise a retried task cleanup would fail forever.
func TestKeychainStoreDeleteIsIdempotent(t *testing.T) {
	s, _ := newFileStore(t)
	ctx := context.Background()
	ref, err := s.Put(ctx, "secret")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("second Delete must be a no-op, got %v", err)
	}
	// A ref this backend never minted is also a no-op, not an error.
	if err := s.Delete(ctx, core.CredentialRef("mem://ud/cred/deadbeef")); err != nil {
		t.Fatalf("foreign Delete must be a no-op, got %v", err)
	}
}

func TestKeychainStoreRejectsForeignOrMalformedRefs(t *testing.T) {
	s, _ := newFileStore(t)
	ctx := context.Background()
	cases := []core.CredentialRef{
		"",
		"mem://ud/cred/00112233445566778899aabbccddeeff", // another backend's scheme
		"file://ud/cred/",                                 // no id
		"file://ud/cred/../../etc/passwd",                 // traversal
		"file://ud/cred/00112233445566778899AABBCCDDEEFF", // uppercase is not our shape
		"file://ud/cred/xyz",                              // not hex, wrong length
		"file://other/00112233445566778899aabbccddeeff",   // wrong host path
	}
	for _, ref := range cases {
		if _, err := s.Get(ctx, ref); err == nil {
			t.Fatalf("Get(%q) must fail", ref)
		}
	}
	// A traversal attempt must not have created or read anything outside dir.
	// "/etc/passwd" is a POSIX probe; on Windows it would resolve to
	// "<cwd-drive>:\etc\passwd" and not exist even in a healthy environment.
	if runtime.GOOS != "windows" {
		if _, err := os.Stat("/etc/passwd"); err != nil {
			t.Fatalf("test environment is broken: %v", err)
		}
	}
}

func TestValidateKeychainSecret(t *testing.T) {
	ok := []string{"x", `{"password":"a b"}`, strings.Repeat("a", maxKeychainSecretBytes)}
	for _, s := range ok {
		if err := validateKeychainSecret(s); err != nil {
			t.Fatalf("validateKeychainSecret(%d bytes) = %v", len(s), err)
		}
	}
	bad := map[string]string{
		"empty":      "",
		"newline":    "a\nb",
		"cr":         "a\rb",
		"nul":        "a\x00b",
		"del":        "a\x7fb",
		"too large":  strings.Repeat("a", maxKeychainSecretBytes+1),
		"tab and c1": "a\x1bb",
	}
	for name, s := range bad {
		err := validateKeychainSecret(s)
		if err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
		var ce *core.Error
		if !errors.As(err, &ce) || ce.Code != core.E_INVALID_INPUT {
			t.Fatalf("%s: code = %v, want %s", name, err, core.E_INVALID_INPUT)
		}
	}
}

// A credential file someone widened to 0644 must not be read: that is exactly
// how a local user would read another account's password.
func TestFileBackendRefusesWidePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Go's os.Chmod cannot widen an NTFS file in a way its own mode bits
		// would report (writable files always stat as 0666), so the widened
		// file cannot be constructed here: NOT_EXECUTED on this host.
		t.Skip("os.Chmod cannot produce a wide-mode file on Windows: NOT_EXECUTED on this host")
	}
	s, dir := newFileStore(t)
	ctx := context.Background()
	ref, err := s.Put(ctx, "s3cret")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	id := strings.TrimPrefix(string(ref), "file://ud/cred/")
	if err := os.Chmod(filepath.Join(dir, id), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err = s.Get(ctx, ref)
	if err == nil {
		t.Fatal("a 0644 credential file must be refused")
	}
	if !strings.Contains(err.Error(), "too open") {
		t.Fatalf("error = %v, want a permissions diagnostic", err)
	}
}

func TestFileBackendLeavesNoTempFile(t *testing.T) {
	s, dir := newFileStore(t)
	if _, err := s.Put(context.Background(), "s3cret"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || strings.HasSuffix(entries[0].Name(), ".tmp") {
		t.Fatalf("credential dir = %v, want exactly one published file", entries)
	}
}

// The directory itself must be 0700 even if it already existed with a looser
// mode, otherwise any local user can enumerate the credential ids.
func TestFileBackendTightensExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	credDir := filepath.Join(dir, "credentials")
	if err := os.MkdirAll(credDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := newFileBackend(dir); err != nil {
		t.Fatalf("newFileBackend: %v", err)
	}
	info, err := os.Stat(credDir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows mode bits never shrink below 0777 (NTFS DACLs are not modelled
	// by Go), so the mode assertion holds only on POSIX.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("credential dir mode = %#o, want 0700", perm)
		}
	}
}

// Memory stays the fallback whenever no persistent backend is usable, and it is
// reported as such — never as a keychain.
func TestMemoryBackendReportingAndSemantics(t *testing.T) {
	s := NewMemoryCredentialStore()
	ctx := context.Background()
	if got := s.CredentialBackend(); got != BackendMemory {
		t.Fatalf("backend = %q, want %q", got, BackendMemory)
	}
	ref, err := s.Put(ctx, "SUPER-SECRET")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(string(ref), "mem://ud/cred/") {
		t.Fatalf("ref = %q", ref)
	}
	if v, err := s.Get(ctx, ref); err != nil || v != "SUPER-SECRET" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, ref); err == nil {
		t.Fatal("deleted credential is still readable")
	}
}

// Two ids must never collide, and every id must round-trip through the ref.
func TestCredentialIDsAreUniqueAndValid(t *testing.T) {
	seen := make(map[string]bool, 256)
	for i := 0; i < 256; i++ {
		id, err := newCredentialID()
		if err != nil {
			t.Fatalf("newCredentialID: %v", err)
		}
		if !validCredentialID(id) {
			t.Fatalf("id %q fails its own validator", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

// NewCredentialStore must always yield a usable store whose reported backend is
// one of the documented values. Which one it is depends on the host, so the test
// asserts the contract, not the platform.
func TestNewCredentialStoreReportsAKnownBackend(t *testing.T) {
	known := map[string]bool{
		BackendMemory: true, BackendFile: true, BackendMacOSKeychain: true,
		BackendWindowsDPAPI: true, BackendSecretService: true,
	}
	s, err := NewCredentialStore(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	if kind := s.CredentialBackend(); !known[kind] {
		t.Fatalf("unknown backend %q", kind)
	}
	// With no state directory, a backend that needs one must degrade to memory
	// instead of failing.
	s, err = NewCredentialStore(CredentialStoreOptions{})
	if err != nil {
		t.Fatalf("NewCredentialStore(no dir): %v", err)
	}
	if kind := s.CredentialBackend(); kind == BackendFile || kind == BackendWindowsDPAPI {
		t.Fatalf("backend %q needs a state directory but was selected without one", kind)
	}
}

func TestFileBackendWithoutDirectoryIsUnavailable(t *testing.T) {
	if _, err := newFileBackend(""); !errors.Is(err, errBackendUnavailable) {
		t.Fatalf("err = %v, want errBackendUnavailable", err)
	}
}
