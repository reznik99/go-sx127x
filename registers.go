package lora

// SX1276 register addresses and constants.
//
// Datasheet: "SX1276/77/78/79 - 137 MHz to 1020 MHz Low Power Long Range Transceiver"
// https://cdn-shop.adafruit.com/product-files/3179/sx1276_77_78_79.pdf
//
// To read a register:  send [addr, 0x00],       receive [_, value]
// To write a register: send [addr|0x80, value], receive [_, _]
// Burst reads/writes auto-increment the address.

// --- Register Addresses ---
const (
	regFifo              = 0x00 // shared TX/RX FIFO data buffer
	regOpMode            = 0x01 // operating mode (sleep/standby/TX/RX)
	regFrfMsb            = 0x06 // carrier frequency MSB
	regFrfMid            = 0x07 // carrier frequency mid
	regFrfLsb            = 0x08 // carrier frequency LSB
	regPaConfig          = 0x09 // power amplifier config
	regLna               = 0x0C // low-noise amplifier (RX sensitivity)
	regFifoAddrPtr       = 0x0D // current FIFO read/write position
	regFifoTxBaseAddr    = 0x0E // FIFO start address for TX
	regFifoRxBaseAddr    = 0x0F // FIFO start address for RX
	regFifoRxCurrentAddr = 0x10 // start of last received packet in FIFO
	regIrqFlags          = 0x12 // interrupt flags (write 1 to clear)
	regRxNbBytes         = 0x13 // payload length of last received packet
	regModemStat         = 0x18 // real-time modem status
	regPktSnrValue       = 0x19 // SNR of last RX packet (signed, /4 = dB)
	regPktRssiValue      = 0x1A // RSSI of last RX packet (-157 + value = dBm at HF)
	regModemConfig1      = 0x1D // bandwidth, coding rate, header mode
	regModemConfig2      = 0x1E // spreading factor, CRC enable
	regPreambleMsb       = 0x20 // preamble length MSB
	regPreambleLsb       = 0x21 // preamble length LSB
	regPayloadLength     = 0x22 // TX payload size in bytes
	regModemConfig3      = 0x26 // AGC, low data rate optimisation
	regSyncWord          = 0x39 // network identifier
	regDioMapping1       = 0x40 // DIO pin event mapping
	regVersion           = 0x42 // chip version (should read 0x12)
	regPaDac             = 0x4D // high-power +20 dBm mode toggle
)

// --- Operating Modes (bits 2:0 of regOpMode) ---
const (
	modeSleep        = 0x00
	modeStandby      = 0x01
	modeTx           = 0x03
	modeRxContinuous = 0x05

	flagLoRaMode = 0x80 // bit 7 of regOpMode — must be set for LoRa
)

// --- IRQ Flag Bits (regIrqFlags) ---
const (
	irqTxDone          = 0x08
	irqValidHeader     = 0x10
	irqPayloadCrcError = 0x20
	irqRxDone          = 0x40
	irqAllFlags        = 0xFF // bitmask to clear all
)

// --- Modem Status Bits (regModemStat) ---
const (
	modemStatHeaderValid = 0x08 // valid header detected (real packet, not noise)
)

// --- DIO0 Pin Mapping (bits 7:6 of regDioMapping1) ---
const (
	dio0RxDone = 0x00 // DIO0 fires on packet received
	dio0TxDone = 0x40 // DIO0 fires on transmission finished
)

// --- SPI Protocol ---
const spiWriteBit = 0x80 // OR with address to write a register

// --- Misc ---
const (
	expectedVersion = 0x12 // regVersion should return this for SX1276/SX1278
	maxPayload      = 255  // SX1276 hardware FIFO limit
	defaultPaDac    = 0x84 // standard PA DAC (no +20 dBm boost)
)
