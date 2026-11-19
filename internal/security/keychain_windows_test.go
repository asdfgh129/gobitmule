//go:build windows

package security

// Real-machine evidence for the Windows DPAPI credential backend
// (docs/23 §六 listed "Windows / Linux 钥匙串真机" as NOT_EXECUTED; this file
// closes the Windows half on an actual logged-in host). Everything below
// drives the REAL CryptProtectData / CryptUnprotectData syscalls from
// crypt32.dll — no mock, no fixture. On a Linux/macOS build these tests do
// not compile in, which is exactly the point: they can only PASS on Windows.

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// A plain round trip must come back byte-identical through the OS primitive,
// including payload shapes a JSON credential blob really contains (multi-byte
// UTF-8, quotes, embedded JSON braces).
func TestDPAPIRealMachineRoundTrip(t *testing.T) {
	plain := []byte(`{"username":"alice","password":"s3cret-密码-🔐","nested":{"a":1}}`)
	sealed, err := dpapiProtect(plain)
	if err != nil {
		t.Fatalf("CryptProtectData: %v", err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("ciphertext contains the plaintext verbatim")
	}
	got, err := dpapiUnprotect(sealed)
	if err != nil {
		t.Fatalf("CryptUnprotectData: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: got %q, want %q", got, plain)
	}
}

// Credential Manager caps generic blobs at 5x512 bytes; DPAPI — the primitive
// Credential Manager itself is built on — must handle far larger payloads
// (a PEM private key is the documented use case, ADR-0015 ④).
func TestDPAPIRealMachineLargeBlob(t *testing.T) {
	plain := []byte(strings.Repeat("ud-dpapi-evidence-", 512)) // 9216 bytes
	sealed, err := dpapiProtect(plain)
	if err != nil {
		t.Fatalf("protect 9KiB blob: %v", err)
	}
	got, err := dpapiUnprotect(sealed)
	if err != nil {
		t.Fatalf("unprotect 9KiB blob: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("large blob round trip mismatch (%d bytes)", len(got))
	}
}

// DPAPI output is integrity-protected: a flipped byte must fail the unprotect
// with an error, never return garbage plaintext.
func TestDPAPIRealMachineTamperedCiphertext(t *testing.T) {
	sealed, err := dpapiProtect([]byte("tamper me"))
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	sealed[len(sealed)/2] ^= 0xFF
	if got, err := dpapiUnprotect(sealed); err == nil {
		t.Fatalf("tampered ciphertext unprotect returned %q, want an error", got)
	}
}

// End to end through KeychainStore with the REAL system backend selection:
// the store must report windows-dpapi, mint dpapi:// refs, and round-trip a
// credential into the per-user profile — the same path POST /credentials uses.
func TestDPAPIBackendStoreEndToEnd(t *testing.T) {
	s, err := NewCredentialStore(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	if got := s.CredentialBackend(); got != BackendWindowsDPAPI {
		t.Fatalf("CredentialBackend = %q, want %q (real DPAPI must be selected when a state dir exists)",
			got, BackendWindowsDPAPI)
	}
	ctx := context.Background()
	secret := `{"username":"bob","password":"pw-evidence"}`
	ref, err := s.Put(ctx, secret)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	const prefix = "dpapi://ud/cred/"
	if !strings.HasPrefix(string(ref), prefix) {
		t.Fatalf("ref = %q, want the %s prefix", ref, prefix)
	}
	if strings.Contains(string(ref), "pw-evidence") {
		t.Fatal("secret leaked into the reference")
	}
	if got, err := s.Get(ctx, ref); err != nil || got != secret {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, ref); err == nil {
		t.Fatal("deleted credential is still readable")
	}
}

// The ciphertext file on disk must not contain the plaintext: the sealed blob
// is what survives a restart, DPAPI is what makes it unreadable to others.
func TestDPAPIRealMachineSealedFileStaysOpaque(t *testing.T) {
	s, err := NewCredentialStore(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	ctx := context.Background()
	secret := `{"password":"opaque-evidence-pw"}`
	ref, err := s.Put(ctx, secret)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	id := strings.TrimPrefix(string(ref), "dpapi://ud/cred/")
	be, ok := s.be.(*dpapiBackend)
	if !ok {
		t.Fatalf("backend type = %T, want *dpapiBackend", s.be)
	}
	sealed, err := os.ReadFile(be.path(id))
	if err != nil {
		t.Fatalf("read sealed file: %v", err)
	}
	if bytes.Contains(sealed, []byte("opaque-evidence-pw")) {
		t.Fatal("plaintext found in the on-disk ciphertext file")
	}
}
