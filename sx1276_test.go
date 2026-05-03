package lora

import "testing"

func TestBandwidthBits(t *testing.T) {
	cases := []struct {
		hz      int
		want    byte
		wantErr bool
	}{
		{125_000, 0x07, false},
		{250_000, 0x08, false},
		{500_000, 0x09, false},
		{0, 0, true},
		{100_000, 0, true},
		{1_000_000, 0, true},
	}
	for _, c := range cases {
		got, err := bandwidthBits(c.hz)
		if (err != nil) != c.wantErr {
			t.Errorf("bandwidthBits(%d) err = %v, wantErr = %v", c.hz, err, c.wantErr)
		}
		if got != c.want {
			t.Errorf("bandwidthBits(%d) = 0x%02X, want 0x%02X", c.hz, got, c.want)
		}
	}
}

func TestSymbolTimeMs(t *testing.T) {
	// Symbol time = 2^SF / BW (seconds), so (1000 << SF) / BW gives milliseconds.
	cases := []struct {
		sf, bw, want int
	}{
		{7, 125_000, 1},     // SF7  @ 125kHz: ~1ms
		{10, 125_000, 8},    // SF10 @ 125kHz: ~8ms
		{12, 125_000, 32},   // SF12 @ 125kHz: ~32ms (needs low data rate optimisation)
		{12, 500_000, 8},    // SF12 @ 500kHz: ~8ms
	}
	for _, c := range cases {
		if got := symbolTimeMs(c.sf, c.bw); got != c.want {
			t.Errorf("symbolTimeMs(%d, %d) = %d, want %d", c.sf, c.bw, got, c.want)
		}
	}
}

// TestFrequencyEncoding verifies the formula:
//   Frf = (freq * 2^19) / 32_000_000
// For 915 MHz the SX1276 datasheet expects 0xE4C000.
func TestFrequencyEncoding(t *testing.T) {
	cases := []struct {
		freq uint64
		want uint64
	}{
		{915_000_000, 0xE4C000}, // NZ/US ISM
		{868_000_000, 0xD90000}, // EU ISM
		{433_000_000, 0x6C4000}, // Asia ISM
	}
	for _, c := range cases {
		got := (c.freq << 19) / 32_000_000
		if got != c.want {
			t.Errorf("freq encoding for %d Hz = 0x%X, want 0x%X", c.freq, got, c.want)
		}
	}
}
