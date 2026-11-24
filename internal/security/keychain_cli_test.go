//go:build darwin || linux

// Tests for the CLI helper plumbing (runCredentialCLI / helperError /
// limitedWriter / firstLine). It follows keychain_cli.go's build tags: these
// helpers serve only the macOS and Linux keychain backends.

package security

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

func TestHelperErrorClassification(t *testing.T) {
	if err := helperError(credentialCLIResult{}, "op"); err != nil {
		t.Fatalf("nil failure must map to nil, got %v", err)
	}
	err := helperError(credentialCLIResult{err: context.DeadlineExceeded}, "op")
	if err == nil || !msgHas(err.Error(), "timed out") {
		t.Fatalf("deadline = %v", err)
	}
	err = helperError(credentialCLIResult{
		err: errFakeExit(1), exitCode: 1, stderr: "boom\nsecond line\n",
	}, "op")
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.E_INTERNAL {
		t.Fatalf("exit failure = %v, want E_INTERNAL", err)
	}
	// Only the first stderr line is kept and the exit code is named.
	if msg := ce.Message; msg != "op failed (exit 1): boom" {
		t.Fatalf("message = %q", msg)
	}
	// Without stderr the wrapped error text is used instead of an empty detail.
	err = helperError(credentialCLIResult{err: errFakeExit(7), exitCode: 7}, "op")
	if !errors.As(err, &ce) || !msgHas(ce.Message, "op failed (exit 7)") {
		t.Fatalf("message without stderr = %v", err)
	}
}

func TestLimitedWriterDropsOverflow(t *testing.T) {
	var buf bytes.Buffer
	w := &limitedWriter{buf: &buf, limit: 4}
	if n, err := w.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if buf.String() != "abcd" {
		t.Fatalf("buffer = %q, want the first 4 bytes", buf.String())
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("Write after overflow: %v", err)
	}
	if buf.String() != "abcd" {
		t.Fatalf("buffer grew after overflow: %q", buf.String())
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"one":                 "one",
		"one\ntwo":            "one",
		"  padded  \nrest":    "padded",
		"security: bad thing": "security: bad thing",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Fatalf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeExitErr struct{ code int }

func (e fakeExitErr) Error() string { return "fake exit" }

func errFakeExit(code int) error { return fakeExitErr{code: code} }

func msgHas(s, sub string) bool { return strings.Contains(s, sub) }
