package lora

import (
	"context"
	"errors"
	"fmt"
	"time"

	"periph.io/x/conn/v3/gpio"
)

// init resets the chip and applies the Config.
func (r *Radio) init() error {
	// Pulse RESET low for 10ms to ensure a clean state, then wait for boot.
	if err := r.resetPin.Out(gpio.Low); err != nil {
		return fmt.Errorf("reset low: %w", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := r.resetPin.Out(gpio.High); err != nil {
		return fmt.Errorf("reset high: %w", err)
	}
	time.Sleep(10 * time.Millisecond)

	// Verify chip is responding.
	if v := r.readReg(regVersion); v != expectedVersion {
		return fmt.Errorf("unexpected chip version: 0x%02X (expected 0x%02X)", v, expectedVersion)
	}

	// LoRa mode bit can only be changed in sleep mode.
	r.writeReg(regOpMode, flagLoRaMode|modeSleep)
	time.Sleep(10 * time.Millisecond)
	r.writeReg(regOpMode, flagLoRaMode|modeStandby)
	time.Sleep(10 * time.Millisecond)

	// Frequency: Frf = (freq * 2^19) / 32_000_000
	frf := (r.cfg.Frequency << 19) / 32_000_000
	r.writeReg(regFrfMsb, byte(frf>>16))
	r.writeReg(regFrfMid, byte(frf>>8))
	r.writeReg(regFrfLsb, byte(frf))

	// Both TX and RX use FIFO start = 0x00. We're half-duplex, so they share.
	r.writeReg(regFifoTxBaseAddr, 0x00)
	r.writeReg(regFifoRxBaseAddr, 0x00)

	// LNA: max gain + HF boost for best receive sensitivity.
	r.writeReg(regLna, 0x23)

	// Modem config 1: bandwidth | coding rate | header mode (explicit = 0).
	bwBits, err := bandwidthBits(r.cfg.Bandwidth)
	if err != nil {
		return err
	}
	if r.cfg.CodingRate < 5 || r.cfg.CodingRate > 8 {
		return fmt.Errorf("invalid coding rate %d (want 5-8)", r.cfg.CodingRate)
	}
	cr := byte(r.cfg.CodingRate-4) << 1 // 5→001, 6→010, 7→011, 8→100
	r.writeReg(regModemConfig1, bwBits<<4|cr)

	// Modem config 2: spreading factor | normal TX | CRC | symb timeout MSB.
	if r.cfg.SpreadingFactor < 7 || r.cfg.SpreadingFactor > 12 {
		return fmt.Errorf("invalid spreading factor %d (want 7-12)", r.cfg.SpreadingFactor)
	}
	cfg2 := byte(r.cfg.SpreadingFactor) << 4
	if r.cfg.EnableCRC {
		cfg2 |= 0x04
	}
	r.writeReg(regModemConfig2, cfg2)

	// Modem config 3: low data rate optimisation if symbol time > 16ms, AGC on.
	cfg3 := byte(0x04) // AGC auto on
	if symbolTimeMs(r.cfg.SpreadingFactor, r.cfg.Bandwidth) > 16 {
		cfg3 |= 0x08
	}
	r.writeReg(regModemConfig3, cfg3)

	// Preamble length.
	r.writeReg(regPreambleMsb, byte(r.cfg.PreambleLength>>8))
	r.writeReg(regPreambleLsb, byte(r.cfg.PreambleLength))

	// Sync word.
	r.writeReg(regSyncWord, r.cfg.SyncWord)

	// PA: PA_BOOST pin, max output. PaConfig = 0x80 | (TxPower - 2).
	if r.cfg.TxPower < 2 || r.cfg.TxPower > 20 {
		return fmt.Errorf("invalid tx power %d (want 2-20)", r.cfg.TxPower)
	}
	r.writeReg(regPaConfig, 0x80|byte(r.cfg.TxPower-2))
	if r.cfg.TxPower > 17 {
		r.writeReg(regPaDac, 0x87) // +20 dBm boost (use sparingly — duty cycle limits)
	} else {
		r.writeReg(regPaDac, defaultPaDac)
	}

	// Verify SPI writes by reading back a key register.
	if got := r.readReg(regSyncWord); got != r.cfg.SyncWord {
		return fmt.Errorf("SPI write verification failed: regSyncWord=0x%02X, want 0x%02X (check MOSI wiring)",
			got, r.cfg.SyncWord)
	}

	return nil
}

// startReceive puts the radio into continuous RX mode. Caller must hold mu.
func (r *Radio) startReceive() {
	r.writeReg(regOpMode, flagLoRaMode|modeStandby)
	r.writeReg(regIrqFlags, irqAllFlags)
	r.writeReg(regFifoAddrPtr, 0x00)
	r.writeReg(regDioMapping1, dio0RxDone)
	// DIO0 is the RxDone interrupt. The SX1276 drives it push-pull, so the
	// pull-down is optional. The sysfs GPIO backend (RK3399/ROCK) can't set a
	// pull and errors out before configuring the edge, so fall back to no pull —
	// otherwise WaitForEdge never fires and RX receives nothing.
	if err := r.dio0Pin.In(gpio.PullDown, gpio.RisingEdge); err != nil {
		if err := r.dio0Pin.In(gpio.Float, gpio.RisingEdge); err != nil {
			r.logger.Warn("dio0 edge config failed", "err", err)
		}
	}
	r.writeReg(regOpMode, flagLoRaMode|modeRxContinuous)
}

// receive blocks until a valid packet is received or ctx is cancelled.
func (r *Radio) receive(ctx context.Context) (Packet, error) {
	// Make sure we're in RX continuous mode.
	r.mu.Lock()
	r.startReceive()
	r.mu.Unlock()

	for {
		// Wait for DIO0 rising edge or ctx cancellation.
		// We poll with a short timeout so ctx cancellation is responsive.
		select {
		case <-ctx.Done():
			return Packet{}, ctx.Err()
		default:
		}

		if !r.dio0Pin.WaitForEdge(100 * time.Millisecond) {
			continue
		}

		// DIO0 fired — read packet under lock.
		r.mu.Lock()
		pkt, ok, err := r.readPacket()
		r.mu.Unlock()

		if err != nil {
			return Packet{}, err
		}
		if !ok {
			continue // spurious edge or noise — keep waiting
		}
		return pkt, nil
	}
}

// readPacket reads a packet from the FIFO. Caller must hold mu.
// Returns ok=false if the IRQ was spurious or the data was noise.
func (r *Radio) readPacket() (Packet, bool, error) {
	flags := r.readReg(regIrqFlags)

	// Spurious wake (e.g. TxDone edge from a concurrent Send) — no RxDone.
	if flags&irqRxDone == 0 {
		return Packet{}, false, nil
	}

	// CRC failure — clear flags and report.
	if flags&irqPayloadCrcError != 0 {
		r.writeReg(regIrqFlags, irqAllFlags)
		return Packet{}, false, errors.New("CRC error in received packet")
	}

	nbBytes := r.readReg(regRxNbBytes)

	// Must be a valid header with non-empty payload, otherwise it's noise.
	if flags&irqValidHeader == 0 || nbBytes == 0 {
		if nbBytes > 0 {
			r.writeReg(regFifoAddrPtr, r.readReg(regFifoRxCurrentAddr))
			noise := r.readBurst(regFifo, int(nbBytes))
			r.logger.Debug("rx noise discarded", "bytes", nbBytes, "data", fmt.Sprintf("%q", noise))
		}
		r.writeReg(regIrqFlags, irqAllFlags)
		return Packet{}, false, nil
	}

	// Read signal quality before touching the FIFO.
	snrRaw := int(int8(r.readReg(regPktSnrValue))) //nolint:gosec // signed conversion is intentional
	rssiRaw := int(r.readReg(regPktRssiValue))
	rssi := -157 + rssiRaw // HF port formula (>500 MHz)
	if snrRaw < 0 {
		rssi += snrRaw / 4 // SX1276 datasheet correction for negative SNR
	}
	snr := snrRaw / 4 // raw value is in 0.25 dB units

	// Point the FIFO at the start of this packet's data and burst-read.
	r.writeReg(regFifoAddrPtr, r.readReg(regFifoRxCurrentAddr))
	data := r.readBurst(regFifo, int(nbBytes))

	// Clear flags. Module stays in RxContinuous, will fire DIO0 again next packet.
	r.writeReg(regIrqFlags, irqAllFlags)

	return Packet{Data: data, RSSI: rssi, SNR: snr}, true, nil
}

// send transmits data, optionally waiting for the channel to clear (LBT).
func (r *Radio) send(ctx context.Context, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Listen Before Talk — wait for in-progress reception to finish.
	if r.cfg.ListenBeforeTalk {
		if err := r.waitChannelClear(ctx); err != nil {
			return err
		}
	}

	// Standby before configuring TX. Brief sleep lets the mode transition settle.
	r.writeReg(regOpMode, flagLoRaMode|modeStandby)
	time.Sleep(5 * time.Millisecond)

	r.writeReg(regIrqFlags, irqAllFlags)
	r.writeReg(regFifoAddrPtr, 0x00)
	r.writeBurst(regFifo, data)
	r.writeReg(regPayloadLength, byte(len(data)))
	r.writeReg(regDioMapping1, dio0TxDone)
	r.writeReg(regOpMode, flagLoRaMode|modeTx)

	// Poll for TxDone. TX is short (<1s at SF7) so polling beats edge-detect here.
	for r.readReg(regIrqFlags)&irqTxDone == 0 {
		select {
		case <-ctx.Done():
			r.writeReg(regOpMode, flagLoRaMode|modeStandby)
			r.writeReg(regIrqFlags, irqAllFlags)
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}

	r.writeReg(regIrqFlags, irqAllFlags)
	r.startReceive()
	return nil
}

// waitChannelClear polls the modem until no header is being decoded, or the
// LBT timeout / ctx expires. Caller must hold mu — this releases and reacquires
// the lock between polls so other operations don't starve.
func (r *Radio) waitChannelClear(ctx context.Context) error {
	deadline := time.Now().Add(r.cfg.LBTTimeout)
	for r.readReg(regModemStat)&modemStatHeaderValid != 0 {
		if time.Now().After(deadline) {
			return errors.New("LBT timeout: channel busy")
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			r.mu.Lock()
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		r.mu.Lock()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// bandwidthBits maps a bandwidth in Hz to the modem config 1 bit pattern.
func bandwidthBits(hz int) (byte, error) {
	switch hz {
	case 125_000:
		return 0x07, nil
	case 250_000:
		return 0x08, nil
	case 500_000:
		return 0x09, nil
	default:
		return 0, fmt.Errorf("unsupported bandwidth %d Hz (want 125000, 250000, or 500000)", hz)
	}
}

// symbolTimeMs returns the symbol duration in milliseconds for a given SF/BW.
// Used to decide whether low data rate optimisation is required.
func symbolTimeMs(sf, bw int) int {
	// Tsym = 2^SF / BW (seconds), so in ms: (1000 << SF) / BW
	return (1000 << sf) / bw
}

// ---------------------------------------------------------------------------
// Low-level SPI helpers
// ---------------------------------------------------------------------------

func (r *Radio) readReg(addr byte) byte {
	tx := []byte{addr & 0x7F, 0x00}
	rx := make([]byte, 2)
	if err := r.conn.Tx(tx, rx); err != nil {
		r.logger.Warn("spi read failed", "reg", fmt.Sprintf("0x%02X", addr), "err", err)
	}
	return rx[1]
}

func (r *Radio) writeReg(addr, val byte) {
	tx := []byte{addr | spiWriteBit, val}
	if err := r.conn.Tx(tx, nil); err != nil {
		r.logger.Warn("spi write failed", "reg", fmt.Sprintf("0x%02X", addr), "err", err)
	}
}

func (r *Radio) readBurst(addr byte, n int) []byte {
	tx := make([]byte, n+1)
	tx[0] = addr & 0x7F
	rx := make([]byte, n+1)
	if err := r.conn.Tx(tx, rx); err != nil {
		r.logger.Warn("spi burst read failed", "reg", fmt.Sprintf("0x%02X", addr), "bytes", n, "err", err)
	}
	return rx[1:]
}

func (r *Radio) writeBurst(addr byte, data []byte) {
	tx := make([]byte, len(data)+1)
	tx[0] = addr | spiWriteBit
	copy(tx[1:], data)
	if err := r.conn.Tx(tx, nil); err != nil {
		r.logger.Warn("spi burst write failed", "reg", fmt.Sprintf("0x%02X", addr), "bytes", len(data), "err", err)
	}
}
