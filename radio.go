// Package lora provides a driver for the Semtech SX1276 LoRa transceiver
// over SPI on Linux (Raspberry Pi or similar).
//
// The package handles only raw byte transport — packet framing and protocol
// design are the caller's responsibility. This keeps the package usable for
// any application (telemetry, chat, mesh, etc.) without dictating wire format.
//
// Typical usage:
//
//	cfg := lora.DefaultConfig()
//	cfg.Frequency = 915_000_000
//	cfg.TxPower = 17
//
//	radio, err := lora.New(cfg)
//	if err != nil { ... }
//	defer radio.Close()
//
//	// Send a packet (blocks until TX done or ctx cancelled)
//	radio.Send(ctx, []byte("hello"))
//
//	// Receive a packet (blocks until RX or ctx cancelled)
//	pkt, err := radio.Receive(ctx)
package lora

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

// Packet is a received message with signal-quality metadata.
type Packet struct {
	Data []byte
	RSSI int // signal strength in dBm (e.g. -120; less negative is stronger)
	SNR  int // signal-to-noise ratio in dB (can be negative; higher is better)
}

// Radio is a connected SX1276 module.
// All methods are safe for concurrent use.
type Radio struct {
	cfg Config

	mu       sync.Mutex
	conn     spi.Conn
	port     spi.PortCloser
	resetPin gpio.PinOut
	dio0Pin  gpio.PinIO

	// logger receives non-fatal diagnostics; never nil after New.
	logger *slog.Logger
}

// New opens a connection to the SX1276 and applies the given configuration.
// Calls host.Init() internally (idempotent — safe if other code also calls it).
func New(cfg Config) (*Radio, error) {
	if _, err := host.Init(); err != nil {
		return nil, fmt.Errorf("host init: %w", err)
	}
	port, err := spireg.Open(cfg.SPIDevice)
	if err != nil {
		return nil, fmt.Errorf("open SPI %s: %w", cfg.SPIDevice, err)
	}

	conn, err := port.Connect(physic.Frequency(cfg.SPISpeed)*physic.Hertz, spi.Mode0, 8)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("SPI connect: %w", err), port.Close())
	}

	resetPin := gpioreg.ByName(cfg.ResetPin)
	if resetPin == nil {
		return nil, errors.Join(fmt.Errorf("GPIO %q not found (reset pin)", cfg.ResetPin), port.Close())
	}

	dio0Pin := gpioreg.ByName(cfg.DIO0Pin)
	if dio0Pin == nil {
		return nil, errors.Join(fmt.Errorf("GPIO %q not found (DIO0 pin)", cfg.DIO0Pin), port.Close())
	}

	r := &Radio{
		cfg:      cfg,
		conn:     conn,
		port:     port,
		resetPin: resetPin,
		dio0Pin:  dio0Pin,
		logger:   cfg.Logger,
	}
	if r.logger == nil {
		r.logger = slog.New(slog.DiscardHandler)
	}

	if err := r.init(); err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("init: %w", err)
	}

	return r, nil
}

// Close waits for any in-flight Send/Receive to finish (via the lock), then
// releases the SPI port. The radio must not be used after Close.
func (r *Radio) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.port.Close()
}

// Send transmits a packet. Blocks until transmission completes, the context is
// cancelled, or LBT times out (if enabled).
//
// Maximum payload size is 255 bytes (SX1276 FIFO limit).
func (r *Radio) Send(ctx context.Context, data []byte) error {
	if len(data) == 0 {
		return errors.New("cannot send empty packet")
	}
	if len(data) > maxPayload {
		return fmt.Errorf("payload too large: %d bytes (max %d)", len(data), maxPayload)
	}
	return r.send(ctx, data)
}

// Receive blocks until a packet arrives or the context is cancelled.
// Returns context.Canceled or context.DeadlineExceeded on cancellation.
func (r *Radio) Receive(ctx context.Context) (Packet, error) {
	return r.receive(ctx)
}
