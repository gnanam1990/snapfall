package testnetops

import "testing"

func TestParseHexBigRejectsSignedQuantities(t *testing.T) {
	for _, value := range []string{"0x-1", "0x+1"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseHexBig(value); err == nil {
				t.Errorf("expected signed quantity %q to fail", value)
			}
		})
	}
}

func TestParseHexBigAcceptsUnsignedQuantity(t *testing.T) {
	got, err := parseHexBig("0x2a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Uint64() != 42 {
		t.Fatalf("quantity = %d, want 42", got.Uint64())
	}
}
