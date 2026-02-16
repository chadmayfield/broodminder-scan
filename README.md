# broodminder-scan

[![CI](https://github.com/chadmayfield/broodminder-scan/actions/workflows/ci.yaml/badge.svg)](https://github.com/chadmayfield/broodminder-scan/actions/workflows/ci.yaml)

Standalone BLE scanners that detect and parse Broodminder beehive sensor advertisements. Useful for validating BLE reading on a Raspberry Pi and for standalone monitoring.

Ported from the [broodminder-diy](https://github.com/dstrickler/broodminder-diy) project (2018 C/Python), updated with support for **all known Broodminder device models** including devices released through 2025.

> **Disclaimer:** These tools are not supported by or affiliated with Broodminder. They are a passion project for personal use.

## Files

| File | Language | Platform | Description |
|------|----------|----------|-------------|
| `main.go` | Go | Linux (primary), macOS (limited) | BLE scanner using `tinygo.org/x/bluetooth` |
| `main_test.go` | Go | — | Unit tests for BLE packet parser |
| `bm-scan.sh` | Bash | Linux only (Raspberry Pi) | BLE scanner using `hcitool` + `hcidump` (BlueZ) |
| `go.mod` | — | — | Go module definition |

## Supported Broodminder Device Models

All 12 known device model byte values are supported:

| Model Byte | Hex | Device Name | Measures | Generation |
|------------|-----|-------------|----------|------------|
| 41 | 0x29 | T | Temperature | Legacy (1st gen) |
| 42 | 0x2A | TH | Temperature + Humidity | Legacy (1st gen) |
| 43 | 0x2B | W | Weight (2 load cells) + Temperature | Legacy (1st gen) |
| 47 | 0x2F | T2 / T3 | Temperature + SwarmMinder | Current |
| 49 | 0x31 | W3 / W4 | Weight (4 load cells) + Temperature | Current |
| 52 | 0x34 | SubHub | BLE relay (mock advertisements) | Current |
| 54 | 0x36 | Hub 4G | Cell gateway (4G/LTE relay to cloud) | Current |
| 56 | 0x38 | TH2 / TH3 | Temperature + Humidity + SwarmMinder | Current |
| 57 | 0x39 | W+ / W2 | Weight (2 load cells) + Temperature | Current |
| 58 | 0x3A | DIY | Weight (4 load cells) + Temperature | Current |
| 60 | 0x3C | Hub WiFi | WiFi gateway (relay to cloud) | Current |
| 63 | 0x3F | BeeDar | Bee flight counter + Acoustic + Temperature | Current |

### Sources

Model information was cross-referenced from multiple independent sources:

- **BroodMinder User Guide v4.50, Appendix B** (official BLE packet spec, November 2021)
- **[sandersmeenk/home_assistant-broodminder](https://github.com/sandersmeenk/home_assistant-broodminder)** (Home Assistant integration, 2025)
- **[doc.mybroodminder.com/30_sensors](https://doc.mybroodminder.com/30_sensors/)** (official sensor overview with model IDs)
- **[dstrickler/broodminder-diy](https://github.com/dstrickler/broodminder-diy)** (original 2018 open-source effort)
- **[wcheswick/BMBase](https://github.com/wcheswick/BMBase)** (BroodMinder base station software)
- **[TotallyGatsby/brood-flow](https://github.com/TotallyGatsby/brood-flow)** (Rust BLE parser)

## BLE Advertisement Packet Format

Broodminder devices broadcast BLE advertisements with manufacturer-specific data. The packet is identified by:

1. **Manufacturer Specific Data** flag: bytes `0x18 0xFF`
2. **IF LLC manufacturer ID**: bytes `0x8D 0x02` (company ID 0x028D = 653)

### Full Packet Layout

| Byte | Index* | Field | Notes |
|------|--------|-------|-------|
| 0-5 | — | MAC Address | Provided by BLE stack |
| 6 | — | `0x18` | AD field length |
| 7 | — | `0xFF` | Manufacturer Specific Data type |
| 8 | — | `0x8D` | IF LLC manufacturer ID (LSB) |
| 9 | — | `0x02` | IF LLC manufacturer ID (MSB) |
| **10** | **0** | **Device Model** | See model table above |
| 11 | 1 | Firmware Minor | Version minor |
| 12 | 2 | Firmware Major | Version major |
| 13 | 3 | Realtime Temp LSB | Models 47+ only |
| 14 | 4 | Battery % | 0-100 |
| 15 | 5 | Elapsed LSB | Sample counter (little-endian) |
| 16 | 6 | Elapsed MSB | |
| 17 | 7 | Temperature LSB | Primary filtered temp (little-endian) |
| 18 | 8 | Temperature MSB | |
| 19 | 9 | Realtime Temp MSB | Models 47+ only |
| 20 | 10 | Weight Left LSB | Little-endian, offset 32767 |
| 21 | 11 | Weight Left MSB | |
| 22 | 12 | Weight Right LSB | Little-endian, offset 32767 |
| 23 | 13 | Weight Right MSB | |
| 24 | 14 | Humidity % | 0 for models without humidity |
| 25 | 15 | Weight L2 LSB / Swarm Time byte 0 | Model-dependent |
| 26 | 16 | Weight L2 MSB / Swarm Time byte 1 | |
| 27 | 17 | Weight R2 LSB / Swarm Time byte 2 | |
| 28 | 18 | Weight R2 MSB / Swarm Time byte 3 | |
| 29 | 19 | RT Total Weight LSB / Swarm State | Model-dependent |
| 30 | 20 | RT Total Weight MSB | |

*Index = offset in manufacturer payload after company ID bytes are stripped (what `tinygo.org/x/bluetooth` provides in its callback).

### Temperature Formulas

**Two different formulas** are used depending on device generation:

#### Legacy models (41, 42, 43) — SHT-like formula

```
raw = byte[8] << 8 | byte[7]     (little-endian uint16)
temp_C = (raw / 65536.0) * 165.0 - 40.0
temp_F = temp_C * 9.0/5.0 + 32.0
```

This is the only formula in the original broodminder-diy code. It matches the Sensirion SHT sensor data encoding.

#### Current models (47, 49, 52, 54, 56, 57, 58, 60, 63) — Centigrade + offset

```
raw = byte[8] << 8 | byte[7]     (little-endian uint16)
temp_C = (raw - 5000) / 100.0
temp_F = temp_C * 9.0/5.0 + 32.0
```

The raw value is centigrade (hundredths of a degree) with a +5000 offset to avoid negative values. This is documented in the BroodMinder User Guide v4.50, Appendix B.

### Weight Formula (all weight-capable models)

```
raw = byte[MSB] << 8 | byte[LSB]     (little-endian uint16)
weight_kg = (raw - 32767) / 100.0
```

**Sentinel values to ignore** (indicate no valid reading):
- `0x7FFF` (32767) — zero offset
- `0x8005` (32773) — common default
- `0xFFFF` (65535) — invalid/missing

Weight is only valid for models 43, 49, 57, and 58. Non-weight devices produce garbage in these bytes.

### Model-Specific Field Availability

| Field | T (41) | TH (42) | W (43) | T2 (47) | W3 (49) | SubHub (52) | TH2 (56) | W+ (57) | DIY (58) | BeeDar (63) |
|-------|--------|---------|--------|---------|---------|-------------|----------|---------|----------|-------------|
| Temperature | SHT | SHT | SHT | Centi | Centi | Centi | Centi | Centi | Centi | Centi |
| Humidity | — | Yes | — | — | — | — | Yes | — | — | — |
| Weight L/R | — | — | Yes | — | Yes | — | — | Yes | Yes | — |
| Weight L2/R2 | — | — | — | — | Yes | — | — | — | Yes | — |
| Realtime Temp | — | — | — | Yes | Yes | — | Yes | Yes | Yes | Yes |
| RT Total Weight | — | — | — | — | Yes | — | — | Yes | Yes | — |
| Swarm State | — | — | — | Yes | — | — | Yes | — | — | — |
| Swarm Time | Yes | Yes | — | — | — | — | — | — | — | — |

### SubHub Behavior

The SubHub (model 52) doesn't have its own sensors. It acts as a BLE relay, retransmitting advertisements from devices it has heard. It creates "mock advertisements" where it cycles through proxied device IDs in bytes 13, 19, and 30.

## Prerequisites

- **Go 1.24+** (for building from source)
- **Linux with BlueZ** (for the shell script: `sudo apt-get install bluez bluez-tools bc`)
- **Root/sudo** required on Linux for BLE scanning privileges

## Building

### Go Scanner

```bash
# Native build (macOS or Linux)
go build -o bm-scan .

# Cross-compile for Raspberry Pi (64-bit, Pi 3/4/5)
GOOS=linux GOARCH=arm64 go build -o bm-scan-linux-arm64 .

# Cross-compile for Raspberry Pi (32-bit, older Pi models)
GOOS=linux GOARCH=arm GOARM=7 go build -o bm-scan-linux-arm .
```

Copy to Pi:
```bash
scp bm-scan-linux-arm64 pi@raspberrypi:~/bm-scan
```

### Shell Script

No build step needed — just copy to the Pi and run:

```bash
scp bm-scan.sh pi@raspberrypi:~/bm-scan.sh
ssh pi@raspberrypi "chmod +x ~/bm-scan.sh"
```

Install dependencies on the Pi:
```bash
sudo apt-get install bluez bluez-tools bc
```

## Usage

Both tools produce the same output format and accept similar flags.

### Go Scanner

```bash
sudo ./bm-scan                     # scan continuously, Fahrenheit
sudo ./bm-scan -duration 30s       # scan for 30 seconds
sudo ./bm-scan -celsius            # show Celsius
sudo ./bm-scan -json               # JSON lines output
sudo ./bm-scan -all                # show all adverts (no dedup)
./bm-scan -version                 # print version and exit
```

### Shell Script

```bash
sudo ./bm-scan.sh                  # scan continuously, Fahrenheit
sudo ./bm-scan.sh -d 30            # scan for 30 seconds
sudo ./bm-scan.sh -c               # show Celsius
sudo ./bm-scan.sh -j               # JSON lines output
sudo ./bm-scan.sh -a               # show all adverts (no dedup)
sudo ./bm-scan.sh -i hci1          # use alternate BLE adapter
```

### Example Output

Human-readable:
```
Scanning for Broodminder BLE devices...
Supported: T, TH, W, T2/T3, TH2/TH3, W+, W3/W4, DIY, SubHub, BeeDar, Hub
Press Ctrl+C to stop
---
Discovered Broodminder device #1: B5:30:07:80:07:00 (W+)
[14:23:15] B5:30:07:80:07:00 W+     FW:2.21  Bat: 92%  Sample:  142  Temp:51.9°F  Wt: L=37.12 R=37.05 Total=74.17 kg
Discovered Broodminder device #2: A3:42:1B:90:03:00 (TH2)
[14:23:16] A3:42:1B:90:03:00 TH2    FW:3.10  Bat: 68%  Sample:   89  Temp:92.0°F  Humidity: 64%
Discovered Broodminder device #3: C1:55:2A:70:05:00 (T2)
[14:23:17] C1:55:2A:70:05:00 T2     FW:3.05  Bat: 71%  Sample:  201  Temp:41.3°F
```

JSON (one object per line):
```json
{"mac":"B5:30:07:80:07:00","rssi":-77,"model":"W+","model_byte":57,"firmware":"2.21","battery_percent":92,"sample_counter":142,"temperature_c":11.06,"temperature_f":51.9,"has_humidity":false,"humidity_pct":0,"has_weight":true,"weight_left":37.12,"weight_right":37.05,"weight_total":74.17,"timestamp":"2026-02-15T14:23:15Z"}
```

## Known Gaps

| Item | Status |
|------|--------|
| **W5** (autumn 2025) | Likely uses model byte 57 (same as W+), but unconfirmed |
| **BeeTV** (camera) | Streams video over WiFi, probably no BLE advertisement |
| **LoRa Hub** | May use model byte 54 (same as Hub 4G) or a new value |
| **WiFi Hub (60)** | Listed in mybroodminder.com docs but not confirmed in HA integration |
| **Weight calibration** | Raw weight values may need per-device calibration factors |
| **SubHub mock data** | SubHub relays are detected but proxied device data is not yet decoded |

## Testing

```bash
go test -race ./...
```

Tests cover the BLE packet parser, temperature formulas (both legacy and current), weight parsing with sentinel detection, model identification, and the deduplication tracker. No Bluetooth hardware needed — tests use synthetic packets.

## How It Works

1. **BLE Scan**: The scanner listens for BLE advertisements
2. **Filter**: Only processes packets with the BroodMinder manufacturer ID (`0x028D`)
3. **Parse**: Extracts sensor data based on the device model byte and applies the correct temperature formula
4. **Dedup**: Skips duplicate readings with the same (MAC, sample counter) pair
5. **Display**: Outputs human-readable or JSON format

The Go version uses `tinygo.org/x/bluetooth` which wraps platform-native BLE APIs (BlueZ on Linux, CoreBluetooth on macOS). The shell script uses raw HCI commands via `hcitool` and `hcidump` (Linux only).

## License

MIT
