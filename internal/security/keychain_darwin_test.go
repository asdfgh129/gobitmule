//go:build darwin

package security

import (
	"context"
	"os"
	"strings"
	"testing"
)

// This is the only OS keychain backend the project can verify on a real machine
// (see docs/06 §9 for why Windows and Linux are cross-compiled and unit-tested
// only). It therefore runs against the real login keychain rather than a fake:
// the point is to prove the argv/stdin handling, the exit-code mapping and the
// escaping all survive contact with /usr/bin/security.
func TestMacOSKeychainRoundTrip(t *testing.T) {
	if _, err := os.Stat(securityCLIPath); err != nil {
		t.Skipf("%s not present: NOT_EXECUTED", securityCLIPath)
	}
	be := &keychainCLIBackend{path: securityCLIPath}
	s := newKeychainStore(be)
	ctx := context.Background()

	if got := s.CredentialBackend(); got != BackendMacOSKeychain {
		t.Fatalf("backend = %q, want %q", got, BackendMacOSKeychain)
	}

	// Spaces, both quote styles, a backslash and '#': everything that a naive
	// quoting scheme mangles.
	secret := `{"username":"a b","password":"p@ss \"quoted\" #hash \\ slash"}`
	ref, err := s.Put(ctx, secret)
	if err != nil {
		if lockedKeychain(err) {
			t.Skipf("login keychain is locked in this session: NOT_EXECUTED (%v)", err)
		}
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), ref) })

	if !strings.HasPrefix(string(ref), "keychain://ud/cred/") {
		t.Fatalf("ref = %q, want the keychain scheme", ref)
	}
	id := strings.TrimPrefix(string(ref), "keychain://ud/cred/")

	got, err := s.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != secret {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, secret)
	}

	// -U gives replace-not-duplicate semantics for a given id. The store always
	// mints a fresh id, so the replace path is exercised at the backend level.
	if err := be.put(ctx, id, "second"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if v, err := s.Get(ctx, ref); err != nil || v != "second" {
		t.Fatalf("after replace Get = %q, %v", v, err)
	}
	// A second id is a separate item; the first must be untouched.
	other, err := s.Put(ctx, "third")
	if err != nil {
		t.Fatalf("Put(third): %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), other) })
	if v, err := s.Get(ctx, ref); err != nil || v != "second" {
		t.Fatalf("after second id Get = %q, %v", v, err)
	}
	if v, err := s.Get(ctx, other); err != nil || v != "third" {
		t.Fatalf("other item Get = %q, %v", v, err)
	}

	// Delete then read: the item must be gone from the OS store, and Get must
	// report "not found" (exit 44), not a backend failure.
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, ref); err == nil {
		t.Fatal("deleted keychain item is still readable")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Get after Delete = %v, want the not-found error", err)
	}
	// Deleting twice must stay a no-op.
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

// The secret must never reach argv: with `security -i` the whole command travels
// on stdin. This test pins the mechanism rather than the outcome, because the
// outcome alone would still pass if the secret were passed with -w.
func TestMacOSKeychainPutUsesStdinNotArgv(t *testing.T) {
	src, err := os.ReadFile("keychain_darwin.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	code := string(src)
	if strings.Contains(code, `"-w", secret`) || strings.Contains(code, `-w `+`" + secret`) {
		t.Fatal("the secret must not be passed as an argv element")
	}
	if !strings.Contains(code, `runCredentialCLI(ctx, b.path, []string{"-i"}, cmd)`) {
		t.Fatal(`the write path must go through "security -i" on stdin`)
	}
}

func TestEscapeSecurityArg(t *testing.T) {
	cases := map[string]string{
		"plain":        "plain",
		"with space":   "with space",
		`quote"here`:   `quote\"here`,
		`back\slash`:   `back\\slash`,
		`mix "and" \x`: `mix \"and\" \\x`,
		"#hash @at !":  "#hash @at !",
	}
	for in, want := range cases {
		if got := escapeSecurityArg(in); got != want {
			t.Fatalf("escapeSecurityArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// lockedKeychain recognises the two messages macOS returns when the login
// keychain cannot be used non-interactively.
func lockedKeychain(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "user interaction is not allowed") ||
		strings.Contains(msg, "locked")
}
