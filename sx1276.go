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

	// Modem-config and PA registers, shared with the runtime SetModem path.
	if _, err := bandwidthBits(r.cfg.Bandwidth); err != nil {
		return err
	}
	if err := validateModem(r.cfg.Modem); err != nil {
		return err
	}
	r.writeModem(r.cfg.Modem)

	// Sync word.
	r.writeReg(regSyncWord, r.cfg.SyncWord)

	// Verify SPI writes by reading back a key register.
	if got := r.readReg(regSyncWord); got != r.cfg.SyncWord {
		return fmt.Errorf("SPI write verification failed: regSyncWord=0x%02X, want 0x%02X (check MOSI wiring)",
			got, r.cfg.SyncWord)
	}

	return nil
}

func validateModem(modem Modem) error {
	if err := validateSpreadingFactor(modem.SpreadingFactor); err != nil {
		return err
	}
	if modem.CodingRate < 5 || modem.CodingRate > 8 {
		return fmt.Errorf("invalid coding rate %d (want 5-8)", modem.CodingRate)
	}
	if modem.TxPower < 2 || modem.TxPower > 20 {
		return fmt.Errorf("invalid tx power %d (want 2-20)", modem.TxPower)
	}
	return nil
}

// writeModem programs the modem-config and PA registers and records them in cfg.
// It touches no operating mode and does not re-arm RX — the caller owns that.
// Shared by init (boot) and SetModem (runtime switch). Caller must hold mu.
func (r *Radio) writeModem(modem Modem) {
	bwBits, _ := bandwidthBits(r.cfg.Bandwidth) // validated by the caller
	cr := byte(modem.CodingRate-4) << 1         // 5→001 .. 8→100
	r.writeReg(regModemConfig1, bwBits<<4|cr)

	cfg2 := byte(modem.SpreadingFactor) << 4
	if r.cfg.EnableCRC {
		cfg2 |= 0x04
	}
	r.writeReg(regModemConfig2, cfg2)

	cfg3 := byte(0x04) // AGC auto on
	if symbolTimeMs(modem.SpreadingFactor, r.cfg.Bandwidth) > 16 {
		cfg3 |= 0x08 // low data rate optimisation
	}
	r.writeReg(regModemConfig3, cfg3)

	r.writeReg(regPreambleMsb, byte(modem.PreambleLength>>8))
	r.writeReg(regPreambleLsb, byte(modem.PreambleLength))
	r.writePA(modem.TxPower)

	r.cfg.Modem = modem
}

// paRegisters returns the PA_BOOST register bytes for txPower. Above +17 dBm the
// OutputPower nibble is already saturated, so the extra +3 dB comes from the
// PaDac high-power bit — and that higher PA current needs OCP raised above its
// ~100 mA reset default. At ≤17 dBm OCP is left alone (setOcp false), matching a
// config that never touches it.
func paRegisters(txPower int) (paConfig, paDac, ocp byte, setOcp bool) {
	if txPower > 17 {
		return 0x80 | byte(txPower-5), 0x87, 0x20 | 0x11, true // OCP on, Imax 140 mA
	}
	return 0x80 | byte(txPower-2), defaultPaDac, 0, false
}

func (r *Radio) writePA(txPower int) {
	paConfig, paDac, ocp, setOcp := paRegisters(txPower)
	r.writeReg(regPaConfig, paConfig)
	r.writeReg(regPaDac, paDac)
	if setOcp {
		r.writeReg(regOcp, ocp)
	}
}

// SetModem switches the on-air parameters at runtime and re-arms RX, under the
// same lock as Send and Receive. A packet the peer sends during the switch may
// be lost, so use it only for coordinated changes — a commanded profile switch
// or a link-recovery rendezvous.
func (r *Radio) SetModem(modem Modem) error {
	if err := validateModem(modem); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeReg(regOpMode, flagLoRaMode|modeStandby)
	time.Sleep(5 * time.Millisecond)
	r.writeModem(modem)
	r.writeReg(regIrqFlags, irqAllFlags)
	r.startReceive()
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

func validateSpreadingFactor(sf int) error {
	if sf < 7 || sf > 12 {
		return fmt.Errorf("invalid spreading factor %d (want 7-12)", sf)
	}
	return nil
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
