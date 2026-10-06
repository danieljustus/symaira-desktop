package main

import (
	"encoding/hex"
	"testing"
)

func TestQuantSidecarWireCases(t *testing.T) {
	for n := 0; n < 8; n++ {
		blob := make([]byte, n)
		captured, err := captureUnpack("test_short", blob)
		if err != nil {
			t.Fatal(err)
		}
		if captured.Error == "" {
			t.Errorf("expected error for blob len %d", n)
		}
	}

	c := captureHeader("test_header", 0x3f800000, 0x40000000, []byte{0x01, 0x02})
	if c.Error != "" {
		t.Fatalf("unexpected error: %v", c.Error)
	}
	if c.PackedHex != hex.EncodeToString([]byte{0x01, 0x02}) {
		t.Errorf("unexpected packed hex: %s", c.PackedHex)
	}
}
