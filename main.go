// broodminder-scan — Broodminder BLE advertisement scanner
//
// Scans for Broodminder BLE advertisements and displays parsed sensor data.
// Supports ALL known Broodminder device models (legacy and current).
//
// Based on:
//   - https://github.com/dstrickler/broodminder-diy (original 2018 C/Python)
//   - BroodMinder User Guide v4.50, Appendix B (official BLE packet spec)
//   - https://github.com/sandersmeenk/home_assistant-broodminder (HA integration)
//   - https://doc.mybroodminder.com/30_sensors/ (official sensor docs)
//
// Build (native):
//   go build -o bm-scan .
//
// Cross-compile for Raspberry Pi (Linux ARM64):
//   GOOS=linux GOARCH=arm64 go build -o bm-scan-linux-arm64 .
//
// Cross-compile for Raspberry Pi (Linux ARM 32-bit, older Pi models):
//   GOOS=linux GOARCH=arm GOARM=7 go build -o bm-scan-linux-arm .
//
// Usage:
//   sudo ./bm-scan                    # scan continuously (Fahrenheit)
//   sudo ./bm-scan -duration 30s      # scan for 30 seconds
//   sudo ./bm-scan -json              # output as JSON lines
//   sudo ./bm-scan -celsius           # show temperature in Celsius
//   sudo ./bm-scan -all               # show all adverts (no dedup)
//
// Requires: Linux with BlueZ (Raspberry Pi, etc.) or macOS with CoreBluetooth.
// Must run as root (sudo) on Linux for BLE scanning privileges.

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"tinygo.org/x/bluetooth"
)

// version is set at build time via -ldflags "-X main.version=v1.0.0"
var version = "dev"

// BroodMinder BLE manufacturer ID (IF LLC, 0x028D = 653)
const broodMinderManufacturerID uint16 = 0x028d

// Device model byte values (byte 10 in full advertisement, index 0 in payload)
// Source: BroodMinder User Guide v4.50 Appendix B + HA integration const.py
const (
	modelT      byte = 41 // 0x29 — Temperature only (1st gen, legacy)
	modelTH     byte = 42 // 0x2A — Temperature + Humidity (1st gen, legacy)
	modelW      byte = 43 // 0x2B — Weight scale, 2 load cells (1st gen, legacy)
	modelT2     byte = 47 // 0x2F — Temperature + SwarmMinder (T2/T3, current)
	modelW3     byte = 49 // 0x31 — Weight scale, 4 load cells (W3/W4, current)
	modelSubHub byte = 52 // 0x34 — SubHub BLE relay (mock advertisements)
	modelHub4G  byte = 54 // 0x36 — Cell Hub / Hub 4G / Hub 4G Weather / Hub 4G Solar
	modelTH2    byte = 56 // 0x38 — Temperature + Humidity + SwarmMinder (TH2/TH3, current)
	modelWPlus  byte = 57 // 0x39 — Weight scale, 2 load cells (W+/W2, current)
	modelDIY    byte = 58 // 0x3A — DIY weight scale, 4 load cells
	modelHubWF  byte = 60 // 0x3C — WiFi Hub
	modelBeeDar byte = 63 // 0x3F — BeeDar (bee flight counter + acoustic)
)

// legacyTempModels use the SHT-like temperature formula: (raw/65536)*165 - 40 = °C
// All other models use the centigrade formula: (raw - 5000) / 100 = °C
var legacyTempModels = map[byte]bool{
	modelT:  true,
	modelTH: true,
	modelW:  true,
}

// noHumidityModels always report 0 for humidity (not a real reading)
var noHumidityModels = map[byte]bool{
	modelT:      true,
	modelT2:     true,
	modelW3:     true,
	modelSubHub: true,
}

// weightModels are models that produce valid weight data
var weightModels = map[byte]bool{
	modelW:     true,
	modelWPlus: true,
	modelW3:    true,
	modelDIY:   true,
}

// fourCellWeightModels have 4 load cells (L, R, L2, R2)
var fourCellWeightModels = map[byte]bool{
	modelW3:  true,
	modelDIY: true,
}

// swarmModels report swarm detection state/time in bytes 25-30
var swarmModels = map[byte]bool{
	modelT2:  true,
	modelTH2: true,
}

// Weight sentinel values to ignore
var weightSentinels = map[uint16]bool{
	0x7FFF: true,
	0x8005: true,
	0xFFFF: true,
}

// Reading holds a parsed BLE advertisement from a Broodminder device.
type Reading struct {
	MAC            string    `json:"mac"`
	RSSI           int16     `json:"rssi"`
	Model          string    `json:"model"`
	ModelByte      byte      `json:"model_byte"`
	FirmwareMinor  byte      `json:"-"`
	FirmwareMajor  byte      `json:"-"`
	Firmware       string    `json:"firmware"`
	BatteryPercent int       `json:"battery_percent"`
	SampleCounter  uint16    `json:"sample_counter"`
	TemperatureC   float64   `json:"temperature_c"`
	TemperatureF   float64   `json:"temperature_f"`
	HasHumidity    bool      `json:"has_humidity"`
	HumidityPct    int       `json:"humidity_pct"`
	HasWeight      bool      `json:"has_weight"`
	WeightLeft     float64   `json:"weight_left,omitempty"`
	WeightRight    float64   `json:"weight_right,omitempty"`
	WeightTotal    float64   `json:"weight_total,omitempty"`
	Has4Cell       bool      `json:"has_4cell,omitempty"`
	WeightLeft2    float64   `json:"weight_left_2,omitempty"`
	WeightRight2   float64   `json:"weight_right_2,omitempty"`
	HasRealtime    bool      `json:"has_realtime,omitempty"`
	RealtimeTempC  float64   `json:"realtime_temp_c,omitempty"`
	RealtimeTempF  float64   `json:"realtime_temp_f,omitempty"`
	RealtimeWeight float64   `json:"realtime_weight,omitempty"`
	HasSwarm       bool      `json:"has_swarm,omitempty"`
	SwarmState     int       `json:"swarm_state,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

func modelName(b byte) string {
	switch b {
	case modelT:
		return "T"
	case modelTH:
		return "TH"
	case modelW:
		return "W"
	case modelT2:
		return "T2"
	case modelW3:
		return "W3"
	case modelSubHub:
		return "SubHub"
	case modelHub4G:
		return "Hub4G"
	case modelTH2:
		return "TH2"
	case modelWPlus:
		return "W+"
	case modelDIY:
		return "DIY"
	case modelHubWF:
		return "HubWF"
	case modelBeeDar:
		return "BeeDar"
	default:
		return fmt.Sprintf("?(%d)", b)
	}
}

// parseTemperature converts the raw 16-bit temperature value to Celsius.
// Legacy models (T/TH/W, ids 41-43) use the SHT-like formula.
// Newer models (47+) use centigrade with +5000 offset.
func parseTemperature(model byte, raw uint16) float64 {
	if raw == 0xFFFF {
		return 0 // invalid sentinel
	}
	if legacyTempModels[model] {
		// SHT-like: (raw / 2^16) * 165 - 40 = °C
		return (float64(raw) / 65536.0) * 165.0 - 40.0
	}
	// Centigrade + 5000 offset: (raw - 5000) / 100 = °C
	return (float64(raw) - 5000.0) / 100.0
}

// parseWeight converts raw 16-bit weight value to kg.
// Returns (value, valid). Sentinel values and non-weight models return valid=false.
func parseWeight(model byte, raw uint16) (float64, bool) {
	if !weightModels[model] {
		return 0, false
	}
	if weightSentinels[raw] {
		return 0, false
	}
	kg := (float64(raw) - 32767.0) / 100.0
	return kg, true
}

// parseAdvertisement parses the manufacturer-specific data payload.
// The data starts after the manufacturer ID bytes (0x8d, 0x02),
// so index 0 = byte 10 in the full advertisement = device model byte.
//
// Payload layout (index : full-packet byte : field):
//
//	 0 : 10 : Device Model
//	 1 : 11 : Firmware Minor
//	 2 : 12 : Firmware Major
//	 3 : 13 : Realtime Temp LSB (models 47+)
//	 4 : 14 : Battery %
//	 5 : 15 : Elapsed/Sample Counter LSB
//	 6 : 16 : Elapsed/Sample Counter MSB
//	 7 : 17 : Temperature LSB
//	 8 : 18 : Temperature MSB
//	 9 : 19 : Realtime Temp MSB (models 47+)
//	10 : 20 : Weight Left LSB
//	11 : 21 : Weight Left MSB
//	12 : 22 : Weight Right LSB
//	13 : 23 : Weight Right MSB
//	14 : 24 : Humidity %
//	15 : 25 : Weight Left2 LSB / Swarm Time byte 0
//	16 : 26 : Weight Left2 MSB / Swarm Time byte 1
//	17 : 27 : Weight Right2 LSB / Swarm Time byte 2
//	18 : 28 : Weight Right2 MSB / Swarm Time byte 3
//	19 : 29 : Realtime Total Weight LSB / Swarm State
//	20 : 30 : Realtime Total Weight MSB
func parseAdvertisement(mac string, rssi int16, data []byte) (*Reading, error) {
	if len(data) < 15 {
		return nil, fmt.Errorf("payload too short: got %d bytes, need at least 15", len(data))
	}

	r := &Reading{
		MAC:       strings.ToUpper(mac),
		RSSI:      rssi,
		Timestamp: time.Now(),
	}

	r.ModelByte = data[0]
	r.Model = modelName(data[0])
	r.FirmwareMinor = data[1]
	r.FirmwareMajor = data[2]
	r.Firmware = fmt.Sprintf("%d.%02d", data[2], data[1])

	// Battery (index 4)
	r.BatteryPercent = min(int(data[4]), 100)

	// Sample counter (little-endian uint16 at index 5-6)
	r.SampleCounter = binary.LittleEndian.Uint16(data[5:7])

	// Primary temperature (little-endian uint16 at index 7-8)
	tempRaw := binary.LittleEndian.Uint16(data[7:9])
	r.TemperatureC = math.Round(parseTemperature(r.ModelByte, tempRaw)*100) / 100
	r.TemperatureF = math.Round((r.TemperatureC*9.0/5.0+32.0)*10) / 10

	// Realtime temperature (index 3 = LSB, index 9 = MSB) — models 47+
	if len(data) >= 10 && !legacyTempModels[r.ModelByte] {
		rtRaw := uint16(data[3]) | uint16(data[9])<<8
		if rtRaw != 0xFFFF && rtRaw != 0 {
			r.HasRealtime = true
			r.RealtimeTempC = math.Round(parseTemperature(r.ModelByte, rtRaw)*100) / 100
			r.RealtimeTempF = math.Round((r.RealtimeTempC*9.0/5.0+32.0)*10) / 10
		}
	}

	// Weight left/right (index 10-13)
	if len(data) >= 14 {
		wlRaw := binary.LittleEndian.Uint16(data[10:12])
		wrRaw := binary.LittleEndian.Uint16(data[12:14])

		wl, wlOk := parseWeight(r.ModelByte, wlRaw)
		wr, wrOk := parseWeight(r.ModelByte, wrRaw)
		if wlOk || wrOk {
			r.HasWeight = true
			r.WeightLeft = math.Round(wl*100) / 100
			r.WeightRight = math.Round(wr*100) / 100
			r.WeightTotal = math.Round((r.WeightLeft+r.WeightRight)*100) / 100
		}
	}

	// Humidity (index 14) — skip for models that always report 0
	if len(data) >= 15 {
		if !noHumidityModels[r.ModelByte] {
			hum := int(data[14])
			if hum >= 0 && hum <= 100 {
				r.HasHumidity = true
				r.HumidityPct = hum
			}
		}
	}

	// Extended fields (index 15-20) — 4-cell weight OR swarm time
	if len(data) >= 19 {
		if fourCellWeightModels[r.ModelByte] {
			// 4-cell weight: L2 at 15-16, R2 at 17-18
			wl2Raw := binary.LittleEndian.Uint16(data[15:17])
			wr2Raw := binary.LittleEndian.Uint16(data[17:19])
			wl2, wl2Ok := parseWeight(r.ModelByte, wl2Raw)
			wr2, wr2Ok := parseWeight(r.ModelByte, wr2Raw)
			if wl2Ok || wr2Ok {
				r.Has4Cell = true
				r.WeightLeft2 = math.Round(wl2*100) / 100
				r.WeightRight2 = math.Round(wr2*100) / 100
				// Update total to include all 4 cells
				r.WeightTotal = math.Round((r.WeightLeft+r.WeightRight+r.WeightLeft2+r.WeightRight2)*100) / 100
			}
		}

		if swarmModels[r.ModelByte] && len(data) >= 20 {
			r.HasSwarm = true
			r.SwarmState = int(data[19])
		}
	}

	// Realtime total weight (index 19-20) — weight models with 47+ firmware
	if len(data) >= 21 && weightModels[r.ModelByte] && !legacyTempModels[r.ModelByte] {
		rtWtRaw := binary.LittleEndian.Uint16(data[19:21])
		if !weightSentinels[rtWtRaw] {
			r.RealtimeWeight = (float64(rtWtRaw) - 32767.0) / 100.0
		}
	}

	return r, nil
}

// tracker deduplicates readings by (MAC, SampleCounter)
type tracker struct {
	mu       sync.Mutex
	seen     map[string]uint16 // MAC -> last sample counter
	firstSee map[string]bool   // MAC -> already discovered
}

func newTracker() *tracker {
	return &tracker{
		seen:     make(map[string]uint16),
		firstSee: make(map[string]bool),
	}
}

// isNew returns true if this is a new reading (different sample counter)
func (t *tracker) isNew(mac string, counter uint16) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, ok := t.seen[mac]
	if ok && last == counter {
		return false
	}
	t.seen[mac] = counter
	return true
}

// isFirstDiscovery returns true the first time a MAC is seen
func (t *tracker) isFirstDiscovery(mac string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstSee[mac] {
		return false
	}
	t.firstSee[mac] = true
	return true
}

func printReading(r *Reading, celsius bool, jsonOut bool) {
	if jsonOut {
		b, _ := json.Marshal(r)
		fmt.Println(string(b))
		return
	}

	temp := fmt.Sprintf("%.1f°F", r.TemperatureF)
	if celsius {
		temp = fmt.Sprintf("%.2f°C", r.TemperatureC)
	}

	ts := r.Timestamp.Format("15:04:05")

	// Base line
	line := fmt.Sprintf("[%s] %s %-6s FW:%s  Bat:%3d%%  Sample:%5d  Temp:%s",
		ts, r.MAC, r.Model, r.Firmware, r.BatteryPercent, r.SampleCounter, temp)

	if r.HasHumidity {
		line += fmt.Sprintf("  Humidity:%3d%%", r.HumidityPct)
	}

	if r.HasWeight {
		line += fmt.Sprintf("  Wt: L=%.2f R=%.2f", r.WeightLeft, r.WeightRight)
		if r.Has4Cell {
			line += fmt.Sprintf(" L2=%.2f R2=%.2f", r.WeightLeft2, r.WeightRight2)
		}
		line += fmt.Sprintf(" Total=%.2f kg", r.WeightTotal)
	}

	if r.HasRealtime && r.RealtimeTempC != 0 {
		if celsius {
			line += fmt.Sprintf("  RT:%.2f°C", r.RealtimeTempC)
		} else {
			line += fmt.Sprintf("  RT:%.1f°F", r.RealtimeTempF)
		}
	}

	if r.HasSwarm && r.SwarmState > 0 {
		line += fmt.Sprintf("  Swarm:%d", r.SwarmState)
	}

	fmt.Println(line)
}

func main() {
	duration := flag.Duration("duration", 0, "scan duration (0 = continuous, e.g. 30s, 5m)")
	celsius := flag.Bool("celsius", false, "display temperature in Celsius (default: Fahrenheit)")
	jsonOut := flag.Bool("json", false, "output readings as JSON lines")
	showAll := flag.Bool("all", false, "show all advertisements (don't deduplicate by sample counter)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("bm-scan %s\n", version)
		os.Exit(0)
	}

	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to enable BLE adapter: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: on Linux, run with sudo; on macOS, grant Bluetooth access to Terminal\n")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGINT/SIGTERM for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\nStopping scan...\n")
		cancel()
	}()

	// Handle duration timeout
	if *duration > 0 {
		go func() {
			select {
			case <-time.After(*duration):
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	t := newTracker()
	deviceCount := 0

	if !*jsonOut {
		fmt.Fprintf(os.Stderr, "Scanning for Broodminder BLE devices...\n")
		fmt.Fprintf(os.Stderr, "Supported models: T, TH, W, T2/T3, TH2/TH3, W+, W3/W4, DIY, SubHub, BeeDar, Hub\n")
		if *duration > 0 {
			fmt.Fprintf(os.Stderr, "Duration: %s\n", *duration)
		} else {
			fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop\n")
		}
		fmt.Fprintf(os.Stderr, "---\n")
	}

	err := adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			adapter.StopScan()
			return
		default:
		}

		// Look for manufacturer-specific data
		mfgData := result.ManufacturerData()
		for _, entry := range mfgData {
			if entry.CompanyID != broodMinderManufacturerID {
				continue
			}

			reading, err := parseAdvertisement(
				result.Address.String(),
				result.RSSI,
				entry.Data,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: parse error for %s: %v\n", result.Address.String(), err)
				continue
			}

			if !*showAll && !t.isNew(reading.MAC, reading.SampleCounter) {
				continue
			}

			if t.isFirstDiscovery(reading.MAC) {
				deviceCount++
				if !*jsonOut {
					fmt.Fprintf(os.Stderr, "Discovered Broodminder device #%d: %s (%s)\n",
						deviceCount, reading.MAC, reading.Model)
				}
			}

			printReading(reading, *celsius, *jsonOut)
		}
	})

	if err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "error: scan failed: %v\n", err)
		os.Exit(1)
	}

	if !*jsonOut {
		fmt.Fprintf(os.Stderr, "---\nScan complete. Found %d Broodminder device(s).\n", deviceCount)
	}
}
