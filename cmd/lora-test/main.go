// Command lora-test is a hardware integration and range-test tool for the
// SX1276 radio. Run it on a Raspberry Pi with a radio attached.
//
// For a range test, run mode ping on two Pis and walk apart while watching the
// RSSI/SNR of each received packet. Or run mode tx on one Pi and mode rx on the
// other. Pipe through tee to record a run for later analysis:
//
//	./lora-test -mode ping | tee range.log
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/reznik99/go-sx127x"
)

func main() {
	mode := flag.String("mode", "ping",
		`one of:
    tx   — transmit a packet every -interval; never listens. Use on one Pi
           paired with another running -mode rx.
    rx   — listen forever; print every packet received with RSSI/SNR. Pair
           with another Pi in -mode tx.
    ping — do both at once: transmit every -interval AND listen. Best for
           range tests with two Pis (both see RSSI in each direction).`)
	interval := flag.Duration("interval", time.Second,
		"time between transmissions (tx and ping modes; ignored in rx)")
	freq := flag.Uint64("freq", 0,
		"carrier frequency in Hz (0 keeps the 915 MHz default; both ends must match)")
	spiDevice := flag.String("spi", "", `SPI device path (empty = "/dev/spidev0.0" default)`)
	resetPin := flag.String("reset-pin", "", `GPIO name for RESET (empty = "GPIO25" default; e.g. GPIO157 on a Radxa ROCK 4)`)
	dio0Pin := flag.String("dio0-pin", "", `GPIO name for DIO0 (empty = "GPIO24" default; e.g. GPIO156 on a Radxa ROCK 4)`)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := run(*mode, *interval, *freq, *spiDevice, *resetPin, *dio0Pin, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(mode string, interval time.Duration, freq uint64, spiDevice, resetPin, dio0Pin string, logger *slog.Logger) error {
	cfg := lora.DefaultConfig()
	if freq != 0 {
		cfg.Frequency = freq
	}
	if spiDevice != "" {
		cfg.SPIDevice = spiDevice
	}
	if resetPin != "" {
		cfg.ResetPin = resetPin
	}
	if dio0Pin != "" {
		cfg.DIO0Pin = dio0Pin
	}
	cfg.Logger = logger.With("subsystem", "LORA") // surface driver noise/errors

	radio, err := lora.New(cfg)
	if err != nil {
		return fmt.Errorf("open radio: %w", err)
	}
	defer func() {
		if err := radio.Close(); err != nil {
			logger.Error("radio close failed", "err", err)
		}
	}()

	logger.Info("radio ready",
		"mode", mode, "freq_hz", cfg.Frequency, "sf", cfg.SpreadingFactor,
		"bw_hz", cfg.Bandwidth, "tx_power_dbm", cfg.TxPower)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch mode {
	case "tx":
		transmit(ctx, radio, interval, logger)
	case "rx":
		receive(ctx, radio, logger)
	case "ping":
		go transmit(ctx, radio, interval, logger)
		receive(ctx, radio, logger)
	default:
		return fmt.Errorf("unknown mode %q (want tx, rx, or ping)", mode)
	}
	return nil
}

// transmit sends an incrementing counter packet on each tick until ctx ends.
func transmit(ctx context.Context, radio *lora.Radio, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			seq++
			payload := fmt.Sprintf("lora-test %d", seq)
			start := time.Now()
			if err := radio.Send(ctx, []byte(payload)); err != nil {
				logger.Warn("send failed", "seq", seq, "err", err)
				continue
			}
			logger.Info("sent", "seq", seq, "bytes", len(payload), "tx_ms", time.Since(start).Milliseconds())
		}
	}
}

// receive logs every incoming packet with its signal quality until ctx ends.
func receive(ctx context.Context, radio *lora.Radio, logger *slog.Logger) {
	for {
		pkt, err := radio.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("receive failed", "err", err)
			continue
		}
		logger.Info("received",
			"rssi_dbm", pkt.RSSI, "snr_db", pkt.SNR,
			"bytes", len(pkt.Data), "data", string(pkt.Data))
	}
}
