//go:build darwin || linux

// runCredentialCLI serves only the macOS `security -i` and Linux `secret-tool`
// backends; it must not join the Windows build set, where it would be dead
// code dragging package coverage below the 90% gate (found by the 2026-11-19
// Windows real-machine gates run).

package security

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// credentialCLITimeout bounds one helper invocation. A keychain call must never
// hang the request that triggered it: an unreachable D-Bus session or a locked
// login keychain has to fail loudly within seconds, not block a worker forever.
const credentialCLITimeout = 15 * time.Second

// credentialCLIResult carries everything a backend needs to classify a failure.
type credentialCLIResult struct {
	stdout   string
	stderr   string
	exitCode int
	// err is nil only for exit code 0. A non-zero exit is reported through
	// exitCode as well, so callers can map documented codes (44 = not found,
	// 45 = duplicate) without parsing text.
	err error
}

// runCredentialCLI executes an OS credential helper. The secret, when there is
// one, travels on stdin: putting it in argv would expose it to every local user
// through `ps` for the duration of the call.
func runCredentialCLI(ctx context.Context, path string, args []string, stdin string) credentialCLIResult {
	ctx, cancel := context.WithTimeout(ctx, credentialCLITimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	// The helper's own output is bounded: a misbehaving binary must not be able
	// to grow this process' heap. Keychain payloads are small by construction.
	cmd.Stdout = &limitedWriter{buf: &out, limit: 1 << 20}
	cmd.Stderr = &limitedWriter{buf: &errb, limit: 64 << 10}

	err := cmd.Run()
	res := credentialCLIResult{stdout: out.String(), stderr: errb.String(), err: err}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.exitCode = ee.ExitCode()
	} else if err != nil {
		res.exitCode = -1
	}
	return res
}

// helperError converts a failed invocation into a wire error without ever
// echoing the secret. Only the first stderr line is kept: helpers tend to print
// multi-line diagnostics that add noise, not signal.
func helperError(res credentialCLIResult, what string) error {
	if res.err == nil {
		return nil
	}
	if errors.Is(res.err, context.DeadlineExceeded) {
		return core.Err(core.E_INTERNAL, "%s timed out after %s", what, credentialCLITimeout)
	}
	detail := firstLine(res.stderr)
	if detail == "" {
		detail = res.err.Error()
	}
	return core.Err(core.E_INTERNAL, "%s failed (exit %d): %s", what, res.exitCode, detail)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// limitedWriter keeps at most limit bytes and discards the rest, so a chatty
// helper cannot exhaust memory while we still capture the diagnostic prefix.
type limitedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}
