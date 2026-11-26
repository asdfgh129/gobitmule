package protocol

import "testing"

// The Web console decodes this hex itself, so the bit order has to be spelled
// out: MSB first, matching GetBit. A regression here would silently paint the
// wrong parts as owned.
func TestBitFieldHexString(t *testing.T) {
	b := NewBitField(10)
	b.SetBit(0) // 0x80
	b.SetBit(1) // 0x40
	b.SetBit(9) // second byte, bit 1 -> 0x40
	if got, want := b.HexString(), "c040"; got != want {
		t.Fatalf("HexString() = %q, want %q", got, want)
	}
	if n := len(b.HexString()); n != 4 {
		t.Fatalf("hex length = %d, want 4 for 10 bits", n)
	}
}

// Trailing bits past the declared size must stay clear, otherwise the console
// shows parts the file does not have.
func TestBitFieldHexStringClearsTrailingBits(t *testing.T) {
	b := NewBitField(3)
	b.SetAll()
	if got, want := b.HexString(), "e0"; got != want {
		t.Fatalf("HexString() = %q, want %q", got, want)
	}
	if b.Count() != 3 {
		t.Fatalf("Count() = %d, want 3", b.Count())
	}
}

// A zero-length bitmap is what a peer sends for a file it already completed.
func TestBitFieldHexStringEmpty(t *testing.T) {
	if got := (BitField{}).HexString(); got != "" {
		t.Fatalf("HexString() = %q, want empty", got)
	}
	if n := (BitField{}).Len(); n != 0 {
		t.Fatalf("Len() = %d, want 0", n)
	}
}
