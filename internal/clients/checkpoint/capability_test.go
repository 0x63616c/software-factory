package checkpoint

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestCapabilityMinterUsesThirtyTwoRandomBytes(t *testing.T) {
	raw := bytes.Repeat([]byte{0x5a}, 32)
	minter, err := NewCapabilityMinter(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("NewCapabilityMinter: %v", err)
	}
	got, err := minter.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if got.Reveal() != base64.RawURLEncoding.EncodeToString(raw) {
		t.Fatal("Mint did not encode all random capability bytes")
	}
}

func TestCapabilityMinterRejectsAnUnreadableSource(t *testing.T) {
	minter, err := NewCapabilityMinter(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("NewCapabilityMinter: %v", err)
	}
	if _, err := minter.Mint(); err == nil {
		t.Fatal("Mint succeeded without enough entropy")
	}
}
