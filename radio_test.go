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

func TestSetModemValidation(t *testing.T) {
	r := &Radio{} // no init — invalid values must fail before hardware access

	cases := []struct {
		name    string
		modem   Modem
		wantErr string
	}{
		{"bad sf", Modem{SpreadingFactor: 6, CodingRate: 5, TxPower: 17}, "invalid spreading factor"},
		{"bad cr", Modem{SpreadingFactor: 9, CodingRate: 4, TxPower: 17}, "invalid coding rate"},
		{"bad tx", Modem{SpreadingFactor: 9, CodingRate: 5, TxPower: 21}, "invalid tx power"},
	}
	for _, tc := range cases {
		err := r.SetModem(tc.modem)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: SetModem err = %v, want containing %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestModemGetter(t *testing.T) {
	want := Modem{SpreadingFactor: 9, CodingRate: 6, TxPower: 20, PreambleLength: 12}
	r := &Radio{cfg: Config{Modem: want}}

	if got := r.Modem(); got != want {
		t.Fatalf("Modem() = %+v, want %+v", got, want)
	}
}

// TestPARegisters locks in the PA_BOOST math: the OutputPower nibble saturates
// at +17 dBm, +18..+20 dBm ride the PaDac boost, and OCP is only raised (and only
// touched at all) above +17. A regression here silently under- or over-drives the PA.
func TestPARegisters(t *testing.T) {
	cases := []struct {
		tx              int
		paConfig, paDac byte
		ocp             byte
		setOcp          bool
	}{
		{2, 0x80, defaultPaDac, 0, false},
		{17, 0x8F, defaultPaDac, 0, false}, // identical to a config that never sets OCP
		{18, 0x8D, 0x87, 0x31, true},
		{20, 0x8F, 0x87, 0x31, true}, // OutputPower 15 + PaDac boost = +20 dBm, OCP 140 mA
	}
	for _, tc := range cases {
		paConfig, paDac, ocp, setOcp := paRegisters(tc.tx)
		if paConfig != tc.paConfig || paDac != tc.paDac || ocp != tc.ocp || setOcp != tc.setOcp {
			t.Errorf("paRegisters(%d) = (0x%02X, 0x%02X, 0x%02X, %v), want (0x%02X, 0x%02X, 0x%02X, %v)",
				tc.tx, paConfig, paDac, ocp, setOcp, tc.paConfig, tc.paDac, tc.ocp, tc.setOcp)
		}
	}
}
