package main

import (
	"fmt"
	"strings"
	"testing"
)

// fastRetryDelay removes the real inter-run backoff for these deterministic
// regressions. Tests do not run in parallel, and t.Cleanup restores the value.
func fastRetryDelay(t *testing.T) {
	t.Helper()
	old := loopbackRetryDelay
	loopbackRetryDelay = 0
	t.Cleanup(func() { loopbackRetryDelay = old })
}

func TestRunWithLoopbackRetryPassOnFirstAttempt(t *testing.T) {
	fastRetryDelay(t)
	calls := 0
	v := runWithLoopbackRetry(func() verdict {
		calls++
		return verdict{
			ID: "BX-01", Client: clientName, Status: "PASS",
			Evidence: "sha256 verified against aria2-next",
		}
	})
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (a PASS must never be retried)", calls)
	}
	if v.Status != "PASS" {
		t.Fatalf("status = %q, want PASS", v.Status)
	}
	if v.Evidence != "sha256 verified against aria2-next" {
		t.Fatalf("evidence = %q, want untouched first-attempt evidence", v.Evidence)
	}
}

func TestRunWithLoopbackRetryRetriesOnlyFail(t *testing.T) {
	fastRetryDelay(t)
	calls := 0
	v := runWithLoopbackRetry(func() verdict {
		calls++
		if calls == 1 {
			return verdict{
				ID: "BX-04", Client: clientName, Status: "FAIL",
				Error: "E_TRANSIENT_NETWORK: first-attempt handshake race",
			}
		}
		return verdict{
			ID: "BX-04", Client: clientName, Status: "PASS",
			Evidence: "sha256 verified against libtorrent",
		}
	})
	if calls != maxLoopbackAttempts {
		t.Fatalf("calls = %d, want %d (one bounded retry)", calls, maxLoopbackAttempts)
	}
	if v.Status != "PASS" {
		t.Fatalf("status = %q, want PASS", v.Status)
	}
	if !strings.Contains(v.Evidence, "sha256 verified against libtorrent") {
		t.Fatalf("retry must keep the genuine second-attempt hash evidence: %q", v.Evidence)
	}
	if !strings.Contains(v.Evidence, "第 2/2 次真实执行通过") {
		t.Fatalf("retry count must be visible in the evidence: %q", v.Evidence)
	}
}

func TestRunWithLoopbackRetryNeverWordsNonPassAsPassed(t *testing.T) {
	fastRetryDelay(t)
	for _, status := range []string{"SKIPPED", "UNSUPPORTED"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			v := runWithLoopbackRetry(func() verdict {
				calls++
				if calls == 1 {
					return verdict{
						ID: "BX-06", Client: clientName, Status: "FAIL",
						Error: "E_TRANSIENT_NETWORK: first-attempt handshake race",
					}
				}
				return verdict{
					ID: "BX-06", Client: clientName, Status: status,
					Evidence: "real engine unavailable for the second attempt",
				}
			})
			if calls != maxLoopbackAttempts {
				t.Fatalf("calls = %d, want %d", calls, maxLoopbackAttempts)
			}
			if v.Status != status {
				t.Fatalf("status = %q, want %q (never coerced to PASS)", v.Status, status)
			}
			if strings.Contains(v.Evidence, "通过") {
				t.Fatalf("%s result must not be described as a pass: %q", status, v.Evidence)
			}
			if !strings.Contains(v.Evidence, "不记为 PASS") || !strings.Contains(v.Evidence, status) {
				t.Fatalf("evidence must state the honest final status: %q", v.Evidence)
			}
		})
	}
}

func TestRunWithLoopbackRetryDoesNotRetryNonFailStatuses(t *testing.T) {
	fastRetryDelay(t)
	for _, status := range []string{"SKIPPED", "UNSUPPORTED"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			v := runWithLoopbackRetry(func() verdict {
				calls++
				return verdict{
					ID: "BX-03", Client: clientName, Status: status,
					Evidence: "python-libtorrent not installed",
				}
			})
			if calls != 1 {
				t.Fatalf("calls = %d, want 1 (%s must be returned immediately)", calls, status)
			}
			if v.Status != status || v.Evidence != "python-libtorrent not installed" {
				t.Fatalf("verdict changed: %+v", v)
			}
		})
	}
}

func TestRunWithLoopbackRetryKeepsFailAfterBoundedAttempts(t *testing.T) {
	fastRetryDelay(t)
	calls := 0
	v := runWithLoopbackRetry(func() verdict {
		calls++
		return verdict{
			ID: "BX-02", Client: clientName, Status: "FAIL",
			Error: "hash mismatch after download",
		}
	})
	if calls != maxLoopbackAttempts {
		t.Fatalf("calls = %d, want %d", calls, maxLoopbackAttempts)
	}
	if v.Status != "FAIL" {
		t.Fatalf("status = %q, want FAIL (a bounded failure is never upgraded)", v.Status)
	}
	if v.Error != "hash mismatch after download" {
		t.Fatalf("error = %q, want the original failure reason", v.Error)
	}
	if !strings.Contains(v.Evidence, "hash mismatch after download") {
		t.Fatalf("empty evidence must fall back to Error: %q", v.Evidence)
	}
	if !strings.Contains(v.Evidence, "连续 2 次真实执行均失败，保留 FAIL") {
		t.Fatalf("evidence must record the bounded failure, not hide it: %q", v.Evidence)
	}
}

// --- L4 lab harness bounded retry ----------------------------------------

func fastL4RetryDelay(t *testing.T) {
	t.Helper()
	old := l4CaseRetryDelay
	l4CaseRetryDelay = 0
	t.Cleanup(func() { l4CaseRetryDelay = old })
}

func TestL4WithRetryTransientThenPass(t *testing.T) {
	fastL4RetryDelay(t)
	calls := 0
	v := l4WithRetry(func() l4Verdict {
		calls++
		if calls == 1 {
			return l4Fail("torrent read error: context deadline exceeded", nil)
		}
		return l4Pass("hash verified", map[string]interface{}{"sha256": "abc"})
	})
	if calls != 2 || v.Status != "PASS" {
		t.Fatalf("calls=%d status=%s want 2/PASS", calls, v.Status)
	}
	if v.Evidence["l4_attempts"] != maxL4Attempts {
		t.Fatalf("attempt count not recorded: %+v", v.Evidence)
	}
	if !strings.Contains(fmt.Sprint(v.Evidence["l4_first_attempt"]), "context deadline exceeded") {
		t.Fatalf("first FAIL must be preserved: %+v", v.Evidence)
	}
	if !strings.Contains(v.Reason, "第 2/2 次全新真实执行通过") {
		t.Fatalf("retry wording must be honest: %q", v.Reason)
	}
}

func TestL4WithRetryNeverRetriesNonTransient(t *testing.T) {
	fastL4RetryDelay(t)
	calls := 0
	v := l4WithRetry(func() l4Verdict {
		calls++
		return l4Fail("sha256 mismatch", nil)
	})
	if calls != 1 || v.Status != "FAIL" {
		t.Fatalf("calls=%d status=%s want 1/FAIL", calls, v.Status)
	}
}

func TestL4WithRetryNeverCoercesSkipped(t *testing.T) {
	fastL4RetryDelay(t)
	calls := 0
	v := l4WithRetry(func() l4Verdict {
		calls++
		if calls == 1 {
			return l4Fail("E_TRANSIENT_NETWORK: peer timeout", nil)
		}
		return l4Skip("tool unavailable", nil)
	})
	if calls != 2 || v.Status != "SKIPPED" {
		t.Fatalf("calls=%d status=%s want 2/SKIPPED", calls, v.Status)
	}
	if strings.Contains(v.Reason, "通过") {
		t.Fatalf("a non-PASS retry must never be worded as a pass: %q", v.Reason)
	}
}
