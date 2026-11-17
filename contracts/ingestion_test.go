package contracts

import "testing"

func TestRawKeyRetainsVersionsAndSeparatesMessageIdentities(t *testing.T) {
	first := RawKey("observation-1", []byte(`{"value":1}`))
	if first != RawKey("observation-1", []byte(`{"value":1}`)) {
		t.Fatal("retransmission changed identity")
	}
	if first == RawKey("observation-1", []byte(`{"value":2}`)) {
		t.Fatal("correction overwrote its original raw identity")
	}
	if first == RawKey("observation-2", []byte(`{"value":1}`)) {
		t.Fatal("distinct upstream messages collapsed")
	}
	if RawKey("a\x00", []byte("b")) == RawKey("a", []byte("\x00b")) {
		t.Fatal("ambiguous key/payload boundary")
	}
}
