package goed2k

import "net"

// Reconnect backoff. eMule reconnects to the server list forever, walking the
// list with a short delay; it never gives up after N tries. A single dropped
// login used to be permanent here, which is why one bad port-callback window
// cost the whole session its server-side sources.
const (
	// serverReconnectInitialSeconds is the ramp step used when a caller passes a
	// non-positive reconnect timeout.
	serverReconnectInitialSeconds = 5
	// serverReconnectFastAttempts is how many tries use the ramp
	// (timeout, 2*timeout, 3*timeout, ...) before switching to the slow lane.
	serverReconnectFastAttempts = 5
	// serverReconnectSlowSeconds caps the delay once the ramp is exhausted, so a
	// long server outage costs one connection attempt a minute instead of
	// hammering the server or going silent forever.
	serverReconnectSlowSeconds = 60
)

type ServerConnectionCandidate struct {
	Identifier string
	Address    *net.TCPAddr
}

type ServerConnectionPolicy struct {
	reconnectSecondsTimeout int64
	maxReconnects           int
	iteration               int
	identifier              string
	address                 *net.TCPAddr
	nextConnectTime         int64
}

func NewServerConnectionPolicy(reconnectSecondsTimeout int64, maxReconnects int) ServerConnectionPolicy {
	if maxReconnects <= 0 {
		maxReconnects = serverReconnectFastAttempts
	}
	p := ServerConnectionPolicy{
		reconnectSecondsTimeout: reconnectSecondsTimeout,
		maxReconnects:           maxReconnects,
	}
	p.RemoveConnectCandidates()
	return p
}

func (p *ServerConnectionPolicy) SetConnectCandidate(identifier string, address *net.TCPAddr, currentSessionTime int64) {
	p.iteration = 0
	p.identifier = identifier
	p.address = address
	p.nextConnectTime = currentSessionTime
}

// SetServerConnectionClosed records a failed or dropped connection and schedules
// the next attempt. It never disables retries: the only way to stop reconnecting
// is RemoveConnectCandidates, which is the explicit "user disconnected" path.
func (p *ServerConnectionPolicy) SetServerConnectionClosed(identifier string, address *net.TCPAddr, currentSessionTime int64) {
	if p.identifier == "" || p.identifier != identifier {
		p.iteration = 0
		p.identifier = identifier
		p.address = address
	}
	p.iteration++
	p.nextConnectTime = currentSessionTime + p.retryDelayMillis()
}

// retryDelayMillis is the ramp for the first attempts, then a flat cap.
func (p *ServerConnectionPolicy) retryDelayMillis() int64 {
	base := p.reconnectSecondsTimeout
	if base <= 0 {
		base = serverReconnectInitialSeconds
	}
	if p.iteration <= p.maxReconnects {
		return int64(p.iteration) * base * 1000
	}
	return serverReconnectSlowSeconds * 1000
}

// MarkLoginSucceeded resets the backoff ramp. Without it a server that drops us
// five times over a whole day would be stuck in the slow lane forever.
func (p *ServerConnectionPolicy) MarkLoginSucceeded(identifier string, address *net.TCPAddr) {
	if identifier == "" || p.identifier != identifier {
		return
	}
	if address != nil && p.address != nil && address.String() != p.address.String() {
		return
	}
	p.iteration = 0
}

func (p ServerConnectionPolicy) HasCandidate() bool {
	return p.identifier != "" && p.address != nil
}

func (p ServerConnectionPolicy) HasIterations() bool {
	return p.iteration < p.maxReconnects
}

func (p ServerConnectionPolicy) Iteration() int { return p.iteration }

// MillisecondsUntilRetry is the remaining backoff, for logging.
func (p ServerConnectionPolicy) MillisecondsUntilRetry(currentSessionTime int64) int64 {
	if p.nextConnectTime == -1 {
		return -1
	}
	if p.nextConnectTime <= currentSessionTime {
		return 0
	}
	return p.nextConnectTime - currentSessionTime
}

func (p *ServerConnectionPolicy) RemoveConnectCandidates() {
	p.iteration = p.maxReconnects
	p.identifier = ""
	p.address = nil
	p.nextConnectTime = -1
}

func (p ServerConnectionPolicy) GetConnectCandidate(currentSessionTime int64) *ServerConnectionCandidate {
	if p.nextConnectTime != -1 && p.nextConnectTime <= currentSessionTime {
		return &ServerConnectionCandidate{
			Identifier: p.identifier,
			Address:    p.address,
		}
	}
	return nil
}
