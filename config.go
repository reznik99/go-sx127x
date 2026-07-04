package lora

import (
	"log/slog"
	"time"
)

// Config holds all tunable parameters for the SX1276 radio.
// Use DefaultConfig() and override the fields you care about.
type Config struct {
	// ── Hardware ──

	// SPIDevice is the SPI device path, e.g. "/dev/spidev0.0".
	SPIDevice string

	// SPISpeed is the SPI clock speed in Hz. The SX1276 supports up to 10 MHz,
	// but 1 MHz is safe over jumper wires.
	SPISpeed int

	// ResetPin is the GPIO name connected to the module's RESET pin
	// (e.g. "GPIO25").
	ResetPin string

	// DIO0Pin is the GPIO name connected to the module's DIO0 pin.
	// Used as an interrupt signal for TX/RX completion (e.g. "GPIO24").
	DIO0Pin string

	// ── Radio parameters ──

	// Frequency is the carrier frequency in Hz (e.g. 915_000_000 for 915 MHz).
	// Must be within the regional ISM band (NZ/US: 915, EU: 868, Asia: 433).
	Frequency uint64

	// SpreadingFactor controls range vs speed (7-12).
	// SF7 = fastest (~5.5 kbps), shortest range.
	// SF12 = slowest (~250 bps), longest range.
	SpreadingFactor int

	// Bandwidth in Hz: 125_000 | 250_000 | 500_000.
	// Lower bandwidth = better sensitivity but slower.
	Bandwidth int

	// CodingRate is the forward error correction (5-8, representing 4/5 to 4/8).
	// Higher = more redundancy = better resilience but more overhead.
	CodingRate int

	// TxPower in dBm (2-20). Above 17 requires PA_BOOST high-power mode.
	TxPower int

	// SyncWord is a 1-byte hardware filter; the SX1276 drops packets whose
	// preamble sync word doesn't match. Both peers must use the same value.
	// 0x12 is the "private network" default — shared with anyone else who
	// also didn't change it. 0x34 is reserved for LoRaWAN (do not use).
	// DefaultConfig uses a non-default value to filter ambient noise.
	SyncWord byte

	// PreambleLength is the number of preamble symbols (default 8, range 6-65535).
	// Longer preamble = better sync at the cost of airtime.
	PreambleLength uint16

	// ── Behavior ──

	// EnableCRC appends a CRC to the payload. Strongly recommended.
	EnableCRC bool

	// ListenBeforeTalk waits for the channel to be clear before transmitting.
	// Prevents stomping on in-progress receptions on shared bands.
	ListenBeforeTalk bool

	// LBTTimeout is the maximum time to wait for the channel to clear
	// before giving up and returning an error from Send.
	LBTTimeout time.Duration

	// ── Logging ──

	// Logger receives non-fatal diagnostics (received noise, SPI glitches,
	// DIO0 hiccups); fatal conditions are returned as errors. If nil,
	// diagnostics are discarded.
	Logger *slog.Logger
}

// DefaultConfig returns a sensible starting config for a 915 MHz private network.
// Override the fields you need.
func DefaultConfig() Config {
	return Config{
		SPIDevice:        "/dev/spidev0.0",
		SPISpeed:         1_000_000,
		ResetPin:         "GPIO25",
		DIO0Pin:          "GPIO24",
		Frequency:        915_000_000,
		SpreadingFactor:  7,
		Bandwidth:        125_000,
		CodingRate:       5, // 4/5
		TxPower:          17,
		SyncWord:         0xBA, // arbitrary non-default value; distinct from the 0x12 private default
		PreambleLength:   8,
		EnableCRC:        true,
		ListenBeforeTalk: true,
		LBTTimeout:       2 * time.Second,
	}
}
