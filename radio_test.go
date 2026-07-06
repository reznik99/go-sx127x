package lora

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestSendValidation checks input validation in Send() without needing hardware.
// We construct a Radio with no SPI/GPIO and call Send — the validation runs
// before any hardware access, so it returns the validation error.
func TestSendValidation(t *testing.T) {
	r := &Radio{} // no init — only validation should run

	cases := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"empty", []byte{}, "empty"},
		{"too large", bytes.Repeat([]byte{0xAA}, 256), "too large"},
	}
	for _, c := range cases {
		err := r.Send(context.Background(), c.data)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: got err %v, want containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestSetSpreadingFactorValidation(t *testing.T) {
	r := &Radio{} // no init — invalid values should fail before hardware access

	for _, sf := range []int{0, 6, 13} {
		err := r.SetSpreadingFactor(sf)
		if err == nil || !strings.Contains(err.Error(), "invalid spreading factor") {
			t.Errorf("SetSpreadingFactor(%d) err = %v, want invalid spreading factor", sf, err)
		}
	}
}

func TestGetSpreadingFactor(t *testing.T) {
	r := &Radio{cfg: Config{SpreadingFactor: 9}}

	if got := r.GetSpreadingFactor(); got != 9 {
		t.Fatalf("GetSpreadingFactor() = %d, want 9", got)
	}
}
