# broodminder-scan Architecture

## Overview

broodminder-scan is a standalone BLE scanner that passively listens for Broodminder sensor advertisements and displays parsed sensor readings in real time. It supports all 12 known Broodminder device models. It has no database, no API client, and no persistent storage. Its primary use cases are:

- Validating BLE reception on a Raspberry Pi or development machine
- Standalone real-time monitoring of nearby Broodminder sensors
- Debugging sensor output during development

### File Structure

```
broodminder-scan/
├── main.go                      # Go implementation (all logic in one file)
├── main_test.go                 # Table-driven tests
├── bm-scan.sh                   # Bash alternative (Linux-only, uses hcitool/hcidump)
├── go.mod                       # Go module (single dependency: tinygo bluetooth)
├── go.sum
├── README.md
├── CLAUDE.md                    # Project conventions
├── docs/
│   └── architecture.md          # This file
└── .github/workflows/ci.yaml   # CI and release pipeline
```

All Go code lives in `main.go` and `main_test.go` -- no packages or subdirectories. This is a deliberate single-binary design choice.

---

## Supported Device Models

| Model Byte | Hex | Name | Category | Measurements |
|---|---|---|---|---|
| 41 | 0x29 | T | Legacy (1st gen) | Temperature only |
| 42 | 0x2A | TH | Legacy (1st gen) | Temperature + Humidity |
| 43 | 0x2B | W | Legacy (1st gen) | Weight (2 cells) + Temperature |
| 47 | 0x2F | T2/T3 | Current | Temperature + SwarmMinder |
| 49 | 0x31 | W3/W4 | Current | Weight (4 cells) + Temperature |
| 52 | 0x34 | SubHub | Current | BLE relay (mock advertisements) |
| 54 | 0x36 | Hub 4G | Current | Cell gateway |
| 56 | 0x38 | TH2/TH3 | Current | Temperature + Humidity + SwarmMinder |
| 57 | 0x39 | W+/W2 | Current | Weight (2 cells) + Temperature |
| 58 | 0x3A | DIY | Current | Weight (4 cells) + Temperature |
| 60 | 0x3C | Hub WiFi | Current | WiFi gateway |
| 63 | 0x3F | BeeDar | Current | Bee flight counter + Acoustic + Temperature |

---

## BLE Packet Format

Broodminder sensors broadcast BLE manufacturer-specific data with company ID `0x028D` (IF LLC, decimal 653). The payload layout after the manufacturer ID bytes:

| Index | Field | Type | Notes |
|---|---|---|---|
| 0 | Device Model | uint8 | Model byte (41-63), maps to device name |
| 1 | Firmware Minor | uint8 | |
| 2 | Firmware Major | uint8 | Displayed as "major.minor" |
| 3 | Realtime Temp LSB | uint8 | Models 47+ only |
| 4 | Battery % | uint8 | Clamped to 100 |
| 5-6 | Sample Counter | uint16 LE | Used for deduplication |
| 7-8 | Temperature | uint16 LE | Primary filtered temperature (see formulas below) |
| 9 | Realtime Temp MSB | uint8 | Combined with index 3 for models 47+ |
| 10-11 | Weight Left | uint16 LE | Offset by 32767, divided by 100 for kg |
| 12-13 | Weight Right | uint16 LE | Same encoding as Weight Left |
| 14 | Humidity % | uint8 | 0-100 (ignored for non-humidity models) |
| 15-16 | Weight L2 / Swarm[0-1] | uint16 LE | W3/DIY: additional load cell. T2/TH2: swarm time bytes. |
| 17-18 | Weight R2 / Swarm[2-3] | uint16 LE | W3/DIY: additional load cell. T2/TH2: swarm time bytes. |
| 19-20 | RT Weight / Swarm State | varies | Model-dependent |

---

## Temperature Formulas

Two formulas are used, determined by the `legacyTempModels` map:

**Legacy models (T=41, TH=42, W=43)** -- SHT-like formula:
```
temp_C = (raw / 65536.0) * 165.0 - 40.0
```

**Current models (47+)** -- Centigrade with offset:
```
temp_C = (raw - 5000.0) / 100.0
```

A raw value of `0xFFFF` (65535) is treated as invalid and returns 0.

Both formulas convert to Fahrenheit for display: `temp_F = temp_C * 9/5 + 32`. Celsius is rounded to 2 decimal places, Fahrenheit to 1.

---

## Weight Parsing

Weight is encoded as a uint16 with a 32767 offset:
```
weight_kg = (raw - 32767) / 100.0
```

Three sentinel values indicate invalid/missing readings and are skipped:

| Sentinel | Hex | Meaning |
|---|---|---|
| 32767 | 0x7FFF | Zero offset (default) |
| 32773 | 0x8005 | Common default |
| 65535 | 0xFFFF | Missing/invalid |

Weight models: W (43), W+ (57), W3 (49), DIY (58).

4-cell weight models (W3, DIY) have additional L2/R2 cells at indices 15-18. Total weight sums all four cells.

---

## Model Classification Maps

```go
legacyTempModels     = {41, 42, 43}           // SHT-like temp formula
noHumidityModels     = {41, 47, 49, 52}       // Humidity byte ignored
weightModels         = {43, 57, 49, 58}       // Has weight sensors
fourCellWeightModels = {49, 58}               // W3, DIY: 4 load cells
swarmModels          = {47, 56}               // T2, TH2: swarm detection
weightSentinels      = {0x7FFF, 0x8005, 0xFFFF}
```

---

## Data Structures

### Reading (main.go)

The `Reading` struct contains all parsed fields from a BLE advertisement:

```go
type Reading struct {
    MAC            string    // BLE MAC address (uppercased)
    RSSI           int16     // Signal strength (dBm)
    Model          string    // Human-readable model name
    ModelByte      byte      // Raw model byte
    FirmwareMinor  byte
    FirmwareMajor  byte
    Firmware       string    // "major.minor" display format
    BatteryPercent int       // 0-100
    SampleCounter  uint16    // For deduplication
    TemperatureC   float64
    TemperatureF   float64
    HasHumidity    bool
    HumidityPct    int
    HasWeight      bool
    WeightLeft     float64   // kg
    WeightRight    float64   // kg
    WeightTotal    float64   // kg (sum of all cells)
    Has4Cell       bool      // W3/DIY models
    WeightLeft2    float64   // kg (W3/DIY only)
    WeightRight2   float64   // kg (W3/DIY only)
    HasRealtime    bool      // Models 47+
    RealtimeTempC  float64
    RealtimeTempF  float64
    RealtimeWeight float64   // kg
    HasSwarm       bool      // T2/TH2 models
    SwarmState     int
    Timestamp      time.Time // UTC
}
```

### Tracker (main.go)

Deduplication tracker keyed by MAC address:

```go
type tracker struct {
    mu       sync.Mutex
    seen     map[string]uint16 // MAC -> last sample counter
    firstSee map[string]bool   // MAC -> already discovered
}
```

---

## BLE Scanning Flow (Go)

1. `adapter.Enable()` initializes the BLE adapter (`bluetooth.DefaultAdapter`)
2. Signal handling: SIGINT/SIGTERM cancel the context; `-duration` flag sets a timeout
3. `adapter.Scan()` iterates over `bluetooth.ScanResult` values
4. For each result, `ManufacturerData()` is checked for company ID `0x028d`
5. `parseAdvertisement(mac, rssi, data)` parses the payload into a `Reading`
6. `tracker.isNew(mac, sampleCounter)` deduplicates (skips if same MAC + same counter)
7. `printReading(reading, celsius, jsonOut)` outputs human-readable or JSON

## BLE Scanning Flow (Bash -- bm-scan.sh)

The Bash implementation uses Linux BlueZ tools directly:

1. `hciconfig` resets and brings up the Bluetooth adapter
2. `hcitool` enables passive LE scanning with duplicate reporting
3. `hcidump --raw` outputs raw HCI packets
4. A shell loop accumulates multi-line packets, scans for `18 FF 8D 02` (manufacturer-specific data marker with BroodMinder ID)
5. MAC is extracted from the HCI LE Advertising Report header (bytes 7-12, reversed)
6. RSSI is the last byte of the packet (signed)
7. `parse_reading` parses the payload using the same byte layout and formulas as the Go implementation

---

## Output Formats

**Human-readable** (default):
```
[14:23:15] B5:30:07:80:07:00 W+     FW:2.21  Bat: 92%  Sample:  142  Temp:51.9°F  Wt: L=37.12 R=37.05 Total=74.17 kg
```

**JSON lines** (`-json` flag):
```json
{"mac":"B5:30:07:80:07:00","rssi":-77,"model":"W+","model_byte":57,"firmware":"2.21","battery_percent":92,"sample_counter":142,"temperature_c":11.06,"temperature_f":51.9,"has_humidity":false,"humidity_pct":0,"has_weight":true,"weight_left":37.12,"weight_right":37.05,"weight_total":74.17,"timestamp":"2026-02-15T14:23:15Z"}
```

---

## CLI Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `-duration` | Duration | 0 (continuous) | Scan duration (e.g., `30s`, `5m`) |
| `-celsius` | bool | false | Display temperature in Celsius |
| `-json` | bool | false | Output as JSON lines |
| `-all` | bool | false | Show all advertisements (disable dedup) |
| `-version` | bool | false | Print version and exit |

---

## Build and Release Pipeline

**CI** (`.github/workflows/ci.yaml`) runs on every push to `main` and on PRs:

1. `go mod verify` -- dependency integrity check
2. `go test -race -count=1 ./...` -- tests with race detector
3. `go vet ./...` -- static analysis
4. Native build + cross-compilation for three Linux targets:
   - `linux/arm64` (Raspberry Pi 3/4/5)
   - `linux/arm` GOARM=7 (older Pi models)
   - `linux/amd64`

**Release** triggers on tags matching `v*`:

1. Builds all three Linux targets with version injection: `-ldflags="-s -w -X main.version=$TAG"`
2. Creates a GitHub Release with auto-generated release notes
3. Uploads binaries + `bm-scan.sh` as release assets

**Version injection**: `main.version` is set at build time. Without ldflags, it defaults to `"dev"`.

---

## Testing

Tests in `main_test.go` are table-driven and cover:

- **TestParseTemperature**: Legacy (TH, W) and current (T2) formulas, including freezing point, room temp, negative values, and 0xFFFF sentinel
- **TestParseWeight**: Valid weights across models (W, W+, W3, DIY), non-weight models (TH, T2), and all three sentinel values
- **TestModelName**: All 12 models + unknown byte
- **TestParseAdvertisement_***: Full advertisement parsing for TH (legacy), W+ (current with weight), W3 (4-cell), T2 (swarm), battery clamping, MAC normalization, humidity suppression
- **TestTracker**: Deduplication by (MAC, sample counter)

A `buildPayload()` helper constructs test BLE payloads with correct little-endian encoding.

---

## Key Design Decisions

1. **Single-file architecture**: All code in `main.go` -- no packages, no subdirectories. Keeps the tool simple and easy to understand.
2. **All values metric internally**: Temperature in Celsius, weight in kg. Fahrenheit/pounds are display-only conversions applied at output time.
3. **Deduplication by sample counter**: Each sensor increments a counter per reading. Duplicate advertisements (same MAC + same counter) are suppressed unless `-all` is set.
4. **No configuration files**: Zero config, zero secrets, zero API keys. The tool reads BLE advertisements passively.
5. **Dual implementation**: Go (cross-platform via tinygo bluetooth) and Bash (Linux-only via BlueZ hcitool/hcidump). The Bash script is included in releases as a fallback for environments where Go binaries aren't practical.
