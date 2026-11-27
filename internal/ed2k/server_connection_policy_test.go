package goed2k

import (
	"net"
	"testing"
)

func testServerAddr(t *testing.T, raw string) *net.TCPAddr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", raw)
	if err != nil {
		t.Fatalf("resolve %s: %v", raw, err)
	}
	return addr
}

// A single failed login used to end server connectivity for the rest of the
// process: nothing armed the policy and ReconnectToServer was false.
func TestServerConnectionPolicyAlwaysSchedulesARetry(t *testing.T) {
	addr := testServerAddr(t, "192.0.2.10:4725")
	var policy ServerConnectionPolicy = NewServerConnectionPolicy(5, 5)
	if policy.GetConnectCandidate(0) != nil {
		t.Fatal("a fresh policy must not dial before an explicit Connect")
	}
	policy.SetConnectCandidate("srv", addr, 1000)

	now := int64(1000)
	for attempt := 1; attempt <= 12; attempt++ {
		policy.SetServerConnectionClosed("srv", addr, now)
		wait := policy.MillisecondsUntilRetry(now)
		if wait <= 0 {
			t.Fatalf("attempt %d: retry delay %dms (<=0 means retries were disabled)", attempt, wait)
		}
		if wait > serverReconnectSlowSeconds*1000 {
			t.Fatalf("attempt %d: retry delay %dms exceeds the cap", attempt, wait)
		}
		now += wait
		if policy.GetConnectCandidate(now) == nil {
			t.Fatalf("attempt %d: candidate unavailable after waiting %dms", attempt, wait)
		}
	}
}

func TestServerConnectionPolicyRampsThenCaps(t *testing.T) {
	addr := testServerAddr(t, "192.0.2.11:4725")
	policy := NewServerConnectionPolicy(5, 3)
	policy.SetConnectCandidate("srv", addr, 0)

	want := []int64{5000, 10000, 15000, 60000, 60000}
	for i, expect := range want {
		policy.SetServerConnectionClosed("srv", addr, 0)
		if got := policy.MillisecondsUntilRetry(0); got != expect {
			t.Fatalf("attempt %d: delay %dms, want %dms", i+1, got, expect)
		}
	}
}

func TestServerConnectionPolicyLoginResetsBackoff(t *testing.T) {
	addr := testServerAddr(t, "192.0.2.12:4725")
	policy := NewServerConnectionPolicy(5, 3)
	policy.SetConnectCandidate("srv", addr, 0)
	for i := 0; i < 6; i++ {
		policy.SetServerConnectionClosed("srv", addr, 0)
	}
	if policy.Iteration() <= 1 {
		t.Fatalf("iteration %d did not grow", policy.Iteration())
	}
	policy.MarkLoginSucceeded("srv", addr)
	if policy.Iteration() != 0 {
		t.Fatalf("iteration %d after a successful login, want 0", policy.Iteration())
	}
	policy.SetServerConnectionClosed("srv", addr, 0)
	if got := policy.MillisecondsUntilRetry(0); got != 5000 {
		t.Fatalf("first delay after a successful login is %dms, want 5000ms", got)
	}
}

func TestServerConnectionPolicyIgnoresSuccessForAnotherServer(t *testing.T) {
	addr := testServerAddr(t, "192.0.2.13:4725")
	other := testServerAddr(t, "192.0.2.14:4725")
	policy := NewServerConnectionPolicy(5, 3)
	policy.SetConnectCandidate("srv", addr, 0)
	policy.SetServerConnectionClosed("srv", addr, 0)
	policy.MarkLoginSucceeded("srv", other)
	if policy.Iteration() != 1 {
		t.Fatalf("iteration %d reset by another server's login, want 1", policy.Iteration())
	}
}

func TestServerConnectionPolicyRemoveDisables(t *testing.T) {
	addr := testServerAddr(t, "192.0.2.15:4725")
	policy := NewServerConnectionPolicy(5, 3)
	policy.SetConnectCandidate("srv", addr, 0)
	policy.SetServerConnectionClosed("srv", addr, 0)
	policy.RemoveConnectCandidates()
	if policy.GetConnectCandidate(1 << 40) != nil {
		t.Fatal("an explicitly disconnected server must not be retried")
	}
	if policy.MillisecondsUntilRetry(1<<40) != -1 {
		t.Fatal("disabled policy should report -1 remaining")
	}
}
