package goed2k

import (
	"net"
	"testing"
	"time"
)

// DoRead used to perform exactly one 16 KiB read per PumpIO tick. With a 100ms
// tick that hard-capped every peer at 160 KiB/s no matter what the link could
// carry, which is why a source eMule reads at 1.8 MB/s trickled here. The drain
// loop must pull well past a single chunk in one call.
func TestDoReadDrainsMoreThanOneChunk(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- conn
	}()

	outgoing, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer outgoing.Close()
	incoming := <-accepted
	if incoming == nil {
		t.Fatal("accept failed")
	}
	defer incoming.Close()

	session := NewSession(NewSettings())
	c := NewConnection(session)
	c.socket = outgoing

	const payload = 256 * 1024
	go func() {
		// Write from the accepted side: `outgoing` is the socket we read from.
		_ = incoming.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, _ = incoming.Write(make([]byte, payload))
	}()

	// The writer may still be filling the socket buffer, so take a few pumps and
	// look at the largest single call: the old implementation could never exceed
	// its 16 KiB buffer, whatever the socket held.
	largest := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && largest < 32*1024 {
		before := c.IncomingBytes()
		if err := c.DoRead(); err != nil {
			t.Fatalf("DoRead: %v", err)
		}
		if got := c.IncomingBytes() - before; got > largest {
			largest = got
		}
		if c.IncomingBytes() >= payload {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if largest <= 16*1024 {
		t.Fatalf("a single DoRead pulled %d bytes; the drain loop is not reading past one chunk", largest)
	}
	if got := c.IncomingBytes(); got > payload {
		t.Fatalf("read %d bytes, more than the %d written", got, payload)
	}
}
