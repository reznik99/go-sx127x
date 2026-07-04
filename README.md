# go-sx127x

[![CI](https://github.com/reznik99/go-sx127x/actions/workflows/ci.yml/badge.svg)](https://github.com/reznik99/go-sx127x/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/reznik99/go-sx127x.svg)](https://pkg.go.dev/github.com/reznik99/go-sx127x)
[![Go Report Card](https://goreportcard.com/badge/github.com/reznik99/go-sx127x)](https://goreportcard.com/report/github.com/reznik99/go-sx127x)
![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-blue)
![Radio](https://img.shields.io/badge/radio-LoRa%20SX127x-2E7D32)

A Go driver for the Semtech SX127x family of LoRa transceivers over SPI on Linux
(Raspberry Pi, Radxa ROCK, and similar SBCs).

It handles raw byte transport only: send and receive with signal-quality
metadata. Packet framing and protocol design are up to you. This is not a
LoRaWAN stack.

## Features

- SX127x LoRa mode: configurable frequency, spreading factor, bandwidth, coding
  rate, TX power, sync word, preamble, and CRC.
- Blocking, `context`-aware `Send` / `Receive` (DIO0 interrupt-driven RX).
- Listen-before-talk to avoid stomping in-progress receptions.
- Optional `*slog.Logger` for non-fatal diagnostics.
- All `Radio` methods are safe for concurrent use.
- One dependency: [periph.io](https://periph.io) for SPI/GPIO.

## Supported hardware

| Chip | Status |
|------|--------|
| SX1276 / SX1277 / SX1278 / SX1279 | ✅ Supported. Identical register map, differ only in frequency range |
| SX1272 | ⚠️ Not yet: different `RegModemConfig` bit layout and RSSI offset (`-139` vs `-157`). PRs welcome |

Tested on Raspberry Pi 3B and Radxa ROCK 4 C+ with the Semtech SX1276.

## Install

```bash
go get github.com/reznik99/go-sx127x@latest
```

> **Note on the package name:** the import path ends in `go-sx127x`, but the
> package identifier is `lora`. Import it plainly (or alias it) and call `lora.*`:
>
> ```go
> import lora "github.com/reznik99/go-sx127x"
> ```

## Quick start

```go
package main

import (
	"context"
	"log"

	lora "github.com/reznik99/go-sx127x"
)

func main() {
	cfg := lora.DefaultConfig()
	cfg.Frequency = 915_000_000 // set to your regional ISM band (NZ/US 915, EU 868)

	radio, err := lora.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer radio.Close()

	// Transmit (blocks until TX done or ctx cancelled)
	if err := radio.Send(context.Background(), []byte("hello")); err != nil {
		log.Fatal(err)
	}

	// Receive (blocks until a packet arrives or ctx cancelled)
	pkt, err := radio.Receive(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("got %q  rssi=%ddBm snr=%ddB", pkt.Data, pkt.RSSI, pkt.SNR)
}
```

Max payload is **255 bytes** (`lora.MaxPayloadSize`, the SX127x FIFO limit).
Both ends must share the same frequency, spreading factor, bandwidth, coding
rate, and sync word, or they won't hear each other.

## Configuration

Start from `DefaultConfig()` and override what you need. Defaults:

| Field | Default | Notes |
|-------|---------|-------|
| `SPIDevice` | `/dev/spidev0.0` | SPI bus/CS device path |
| `SPISpeed` | 1 MHz | safe over jumper wires (SX127x supports up to 10 MHz) |
| `ResetPin` | `GPIO25` | GPIO name for the module RESET line |
| `DIO0Pin` | `GPIO24` | GPIO name for the RxDone/TxDone interrupt |
| `Frequency` | 915 MHz | must be within your regional ISM band |
| `SpreadingFactor` | 7 | 7 = fastest/shortest range, 12 = slowest/longest |
| `Bandwidth` | 125 kHz | 125 / 250 / 500 kHz |
| `CodingRate` | 5 (4/5) | 5–8 |
| `TxPower` | 17 dBm | 2–20 (>17 uses PA_BOOST) |
| `SyncWord` | 0xBA | 1-byte filter, both peers must match. Avoid 0x34 (LoRaWAN) |
| `EnableCRC` | true | |
| `ListenBeforeTalk` | true | |

`ResetPin` and `DIO0Pin` take periph.io GPIO names, which differ per board. For
example, wiring RESET to physical pin 22 and DIO0 to physical pin 18:

- **Raspberry Pi:** `GPIO25` / `GPIO24` (the defaults).
- **Radxa ROCK 4 C+:** `GPIO157` / `GPIO156`.

Find the SPI device with `ls /dev/spidev*` and GPIO line numbers with `gpioinfo`.

## Wiring (SX1276 → Raspberry Pi)

| Module | Function | Pi physical pin | Pi GPIO |
|--------|----------|-----------------|---------|
| 3V3 | Power (**3.3V, not 5V**) | 1 | 3.3V |
| GND | Ground | 6 | GND |
| MISO | SPI data out | 21 | GPIO 9 |
| MOSI | SPI data in | 19 | GPIO 10 |
| SCK | SPI clock | 23 | GPIO 11 |
| NSS/CS | Chip select | 24 | GPIO 8 (CE0) |
| RESET | Reset | 22 | GPIO 25 |
| DIO0 | RX/TX interrupt | 18 | GPIO 24 |

Attach an antenna (a 17.3 cm quarter-wave wire works for 915 MHz).

## `lora-test` CLI

A hardware bring-up and range-test tool ships in `cmd/lora-test`:

```bash
go build -o lora-test ./cmd/lora-test

./lora-test -mode rx                 # listen and print packets with RSSI/SNR
./lora-test -mode tx                 # transmit a counter every -interval
./lora-test -mode ping               # do both, best for range tests
./lora-test -mode rx -reset-pin GPIO157 -dio0-pin GPIO156   # non-Pi pin overrides
```

Both ends must use the same `-freq` and spreading factor.

## Permissions

On Raspberry Pi OS the default user is in the `gpio`/`spi` groups and periph.io
uses the memory-mapped `bcm283x` driver, so no `sudo` is needed. On boards
without a memory-mapped periph.io driver (e.g. the RK3399 on a ROCK 4 C+) it
falls back to the Linux **sysfs** GPIO interface, whose pin export requires root.
Run with `sudo`, or add a udev rule granting the `gpio` group access. (The
driver automatically drops the DIO0 pull-down on sysfs backends, which don't
support it.)

## License

[MIT](LICENSE) © Francesco Gorini
