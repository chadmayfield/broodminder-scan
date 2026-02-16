package main

import (
	"encoding/binary"
	"math"
	"testing"
)

// buildPayload constructs a BLE manufacturer data payload for testing.
// This builds the payload starting at index 0 = device model byte (byte 10 in full packet).
func buildPayload(model byte, fwMinor, fwMajor byte, rtTempLSB byte, battery byte,
	elapsed uint16, temp uint16, rtTempMSB byte,
	weightL, weightR uint16, humidity byte,
	wl2, wr2 uint16, swarmOrRtWtL, rtWtH byte,
) []byte {
	p := make([]byte, 21)
	p[0] = model
	p[1] = fwMinor
	p[2] = fwMajor
	p[3] = rtTempLSB
	p[4] = battery
	binary.LittleEndian.PutUint16(p[5:7], elapsed)
	binary.LittleEndian.PutUint16(p[7:9], temp)
	p[9] = rtTempMSB
	binary.LittleEndian.PutUint16(p[10:12], weightL)
	binary.LittleEndian.PutUint16(p[12:14], weightR)
	p[14] = humidity
	binary.LittleEndian.PutUint16(p[15:17], wl2)
	binary.LittleEndian.PutUint16(p[17:19], wr2)
	p[19] = swarmOrRtWtL
	p[20] = rtWtH
	return p
}

func TestParseTemperature(t *testing.T) {
	tests := []struct {
		name  string
		model byte
		raw   uint16
		wantC float64
		tol   float64
	}{
		{
			name:  "legacy TH sensor — freezing point",
			model: modelTH,
			// 0°C: (raw/65536)*165-40 = 0 → raw = (40/165)*65536 ≈ 15887
			raw:   15887,
			wantC: 0.0,
			tol:   0.1,
		},
		{
			name:  "legacy TH sensor — room temp ~22°C",
			model: modelTH,
			// 22°C: (raw/65536)*165-40 = 22 → raw = (62/165)*65536 ≈ 24618
			raw:   24618,
			wantC: 22.0,
			tol:   0.1,
		},
		{
			name:  "legacy W sensor — brood temp ~35°C",
			model: modelW,
			// 35°C: (raw/65536)*165-40 = 35 → raw = (75/165)*65536 ≈ 29789
			raw:   29789,
			wantC: 35.0,
			tol:   0.1,
		},
		{
			name:  "current T2 sensor — freezing point",
			model: modelT2,
			// 0°C: (raw-5000)/100 = 0 → raw = 5000
			raw:   5000,
			wantC: 0.0,
			tol:   0.01,
		},
		{
			name:  "current T2 sensor — room temp 22°C",
			model: modelT2,
			// 22°C: (raw-5000)/100 = 22 → raw = 7200
			raw:   7200,
			wantC: 22.0,
			tol:   0.01,
		},
		{
			name:  "current TH2 sensor — brood temp 35°C",
			model: modelTH2,
			// 35°C: (raw-5000)/100 = 35 → raw = 8500
			raw:   8500,
			wantC: 35.0,
			tol:   0.01,
		},
		{
			name:  "current W+ sensor — negative temp -10°C",
			model: modelWPlus,
			// -10°C: (raw-5000)/100 = -10 → raw = 4000
			raw:   4000,
			wantC: -10.0,
			tol:   0.01,
		},
		{
			name:  "sentinel 0xFFFF returns 0",
			model: modelT2,
			raw:   0xFFFF,
			wantC: 0.0,
			tol:   0.01,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTemperature(tt.model, tt.raw)
			if math.Abs(got-tt.wantC) > tt.tol {
				t.Errorf("parseTemperature(model=%d, raw=%d) = %.4f, want %.4f (±%.4f)",
					tt.model, tt.raw, got, tt.wantC, tt.tol)
			}
		})
	}
}

func TestParseWeight(t *testing.T) {
	tests := []struct {
		name      string
		model     byte
		raw       uint16
		wantKg    float64
		wantValid bool
	}{
		{
			name:      "W sensor — positive weight",
			model:     modelW,
			raw:       32767 + 5000, // 50.00 kg
			wantKg:    50.0,
			wantValid: true,
		},
		{
			name:      "W+ sensor — positive weight",
			model:     modelWPlus,
			raw:       32767 + 7417, // 74.17 kg
			wantKg:    74.17,
			wantValid: true,
		},
		{
			name:      "W3 sensor — 4-cell model valid",
			model:     modelW3,
			raw:       32767 + 2500, // 25.00 kg
			wantKg:    25.0,
			wantValid: true,
		},
		{
			name:      "DIY sensor — valid",
			model:     modelDIY,
			raw:       32767 + 3000, // 30.00 kg
			wantKg:    30.0,
			wantValid: true,
		},
		{
			name:      "TH sensor — not a weight model",
			model:     modelTH,
			raw:       32767 + 5000,
			wantKg:    0,
			wantValid: false,
		},
		{
			name:      "T2 sensor — not a weight model",
			model:     modelT2,
			raw:       32767 + 5000,
			wantKg:    0,
			wantValid: false,
		},
		{
			name:      "sentinel 0x7FFF",
			model:     modelW,
			raw:       0x7FFF,
			wantKg:    0,
			wantValid: false,
		},
		{
			name:      "sentinel 0x8005",
			model:     modelW,
			raw:       0x8005,
			wantKg:    0,
			wantValid: false,
		},
		{
			name:      "sentinel 0xFFFF",
			model:     modelW,
			raw:       0xFFFF,
			wantKg:    0,
			wantValid: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kg, valid := parseWeight(tt.model, tt.raw)
			if valid != tt.wantValid {
				t.Errorf("parseWeight(model=%d, raw=%d) valid = %v, want %v",
					tt.model, tt.raw, valid, tt.wantValid)
			}
			if valid && math.Abs(kg-tt.wantKg) > 0.01 {
				t.Errorf("parseWeight(model=%d, raw=%d) = %.2f kg, want %.2f kg",
					tt.model, tt.raw, kg, tt.wantKg)
			}
		})
	}
}

func TestModelName(t *testing.T) {
	tests := []struct {
		model byte
		want  string
	}{
		{modelT, "T"},
		{modelTH, "TH"},
		{modelW, "W"},
		{modelT2, "T2"},
		{modelW3, "W3"},
		{modelSubHub, "SubHub"},
		{modelHub4G, "Hub4G"},
		{modelTH2, "TH2"},
		{modelWPlus, "W+"},
		{modelDIY, "DIY"},
		{modelHubWF, "HubWF"},
		{modelBeeDar, "BeeDar"},
		{99, "?(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := modelName(tt.model)
			if got != tt.want {
				t.Errorf("modelName(%d) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestParseAdvertisement_TooShort(t *testing.T) {
	_, err := parseAdvertisement("AA:BB:CC:DD:EE:FF", -70, []byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for short payload, got nil")
	}
}

func TestParseAdvertisement_LegacyTH(t *testing.T) {
	// Simulate a legacy TH (model 42) sensor
	// Temperature: 22°C → raw ≈ 24618
	// Humidity: 64%
	// Battery: 68%
	// Elapsed: 89
	payload := buildPayload(
		modelTH, 10, 3, // model=TH, fw=3.10
		0,                    // rt temp LSB (unused for legacy)
		68,                   // battery 68%
		89,                   // elapsed/sample
		24618,                // temperature raw (≈22°C in SHT formula)
		0,                    // rt temp MSB (unused for legacy)
		0x7FFF, 0x7FFF,       // weight sentinels (no weight)
		64,                   // humidity 64%
		0x7FFF, 0x7FFF, 0, 0, // extended fields (unused)
	)

	r, err := parseAdvertisement("A3:42:1B:90:03:00", -55, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.Model != "TH" {
		t.Errorf("model = %q, want %q", r.Model, "TH")
	}
	if r.BatteryPercent != 68 {
		t.Errorf("battery = %d, want 68", r.BatteryPercent)
	}
	if r.SampleCounter != 89 {
		t.Errorf("sample = %d, want 89", r.SampleCounter)
	}
	if math.Abs(r.TemperatureC-22.0) > 0.5 {
		t.Errorf("temp = %.2f°C, want ~22.0°C", r.TemperatureC)
	}
	if !r.HasHumidity {
		t.Error("expected HasHumidity=true for TH sensor")
	}
	if r.HumidityPct != 64 {
		t.Errorf("humidity = %d, want 64", r.HumidityPct)
	}
	if r.HasWeight {
		t.Error("expected HasWeight=false for TH sensor")
	}
	if r.MAC != "A3:42:1B:90:03:00" {
		t.Errorf("mac = %q, want %q", r.MAC, "A3:42:1B:90:03:00")
	}
	if r.Firmware != "3.10" {
		t.Errorf("firmware = %q, want %q", r.Firmware, "3.10")
	}
}

func TestParseAdvertisement_CurrentWPlus(t *testing.T) {
	// Simulate a current W+ (model 57) sensor
	// Temperature: 11°C → raw = 6100 ((6100-5000)/100 = 11.0)
	// Weight L: 37.12 kg → raw = 32767 + 3712 = 36479
	// Weight R: 37.05 kg → raw = 32767 + 3705 = 36472
	// Battery: 92%
	// Elapsed: 142
	payload := buildPayload(
		modelWPlus, 21, 2, // model=W+, fw=2.21
		0x88,               // rt temp LSB
		92,                 // battery 92%
		142,                // elapsed/sample
		6100,               // temperature raw (11.0°C)
		0x13,               // rt temp MSB → rtRaw = 0x1388 = 5000 → 0°C
		36479, 36472,       // weight L=37.12, R=37.05
		0,                  // humidity (0 — W+ has no humidity)
		0x7FFF, 0x7FFF,     // extended weight (sentinel)
		0, 0,               // rt total weight (sentinel bytes)
	)

	r, err := parseAdvertisement("B5:30:07:80:07:00", -77, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.Model != "W+" {
		t.Errorf("model = %q, want %q", r.Model, "W+")
	}
	if r.BatteryPercent != 92 {
		t.Errorf("battery = %d, want 92", r.BatteryPercent)
	}
	if math.Abs(r.TemperatureC-11.0) > 0.01 {
		t.Errorf("temp = %.2f°C, want 11.00°C", r.TemperatureC)
	}
	if !r.HasWeight {
		t.Fatal("expected HasWeight=true for W+ sensor")
	}
	if math.Abs(r.WeightLeft-37.12) > 0.01 {
		t.Errorf("weight_left = %.2f, want 37.12", r.WeightLeft)
	}
	if math.Abs(r.WeightRight-37.05) > 0.01 {
		t.Errorf("weight_right = %.2f, want 37.05", r.WeightRight)
	}
	if math.Abs(r.WeightTotal-74.17) > 0.01 {
		t.Errorf("weight_total = %.2f, want 74.17", r.WeightTotal)
	}
	// W+ is not in noHumidityModels — it can report humidity.
	// With humidity byte = 0, HasHumidity depends on whether 0 is treated as valid.
	if r.Firmware != "2.21" {
		t.Errorf("firmware = %q, want %q", r.Firmware, "2.21")
	}
}

func TestParseAdvertisement_W3FourCell(t *testing.T) {
	// Simulate a W3 (model 49) with 4 load cells
	// Temperature: 20°C → raw = 7000
	// Weight L: 10.00 kg, R: 10.00 kg, L2: 10.00 kg, R2: 10.00 kg
	wRaw := uint16(32767 + 1000) // 10.00 kg each
	payload := buildPayload(
		modelW3, 5, 4, // model=W3, fw=4.05
		0,             // rt temp LSB
		100,           // battery 100%
		500,           // elapsed/sample
		7000,          // temperature raw (20.0°C)
		0,             // rt temp MSB
		wRaw, wRaw,    // weight L, R
		0,             // humidity (0 — W3 has no humidity)
		wRaw, wRaw,    // weight L2, R2
		0, 0,          // rt total weight
	)

	r, err := parseAdvertisement("C1:22:33:44:55:66", -60, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.Model != "W3" {
		t.Errorf("model = %q, want %q", r.Model, "W3")
	}
	if !r.HasWeight {
		t.Fatal("expected HasWeight=true for W3")
	}
	if !r.Has4Cell {
		t.Fatal("expected Has4Cell=true for W3")
	}
	if math.Abs(r.WeightLeft-10.0) > 0.01 {
		t.Errorf("weight_left = %.2f, want 10.00", r.WeightLeft)
	}
	if math.Abs(r.WeightLeft2-10.0) > 0.01 {
		t.Errorf("weight_left_2 = %.2f, want 10.00", r.WeightLeft2)
	}
	if math.Abs(r.WeightTotal-40.0) > 0.01 {
		t.Errorf("weight_total = %.2f, want 40.00 (4 × 10.00)", r.WeightTotal)
	}
	if math.Abs(r.TemperatureC-20.0) > 0.01 {
		t.Errorf("temp = %.2f°C, want 20.00°C", r.TemperatureC)
	}
}

func TestParseAdvertisement_T2Swarm(t *testing.T) {
	// Simulate a T2 (model 47) with swarm state
	// Temperature: 35°C → raw = 8500
	payload := buildPayload(
		modelT2, 5, 3, // model=T2, fw=3.05
		0,             // rt temp LSB
		71,            // battery 71%
		201,           // elapsed/sample
		8500,          // temperature raw (35.0°C)
		0,             // rt temp MSB
		0x7FFF, 0x7FFF, // weight (sentinel — T2 has no weight)
		0,              // humidity (0 — T2 has no humidity)
		0, 0,           // swarm time (wl2, wr2 slots)
		3, 0,           // swarm state = 3, rt total weight MSB
	)

	r, err := parseAdvertisement("D1:44:55:66:77:88", -65, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.Model != "T2" {
		t.Errorf("model = %q, want %q", r.Model, "T2")
	}
	if r.HasWeight {
		t.Error("expected HasWeight=false for T2")
	}
	if r.HasHumidity {
		t.Error("expected HasHumidity=false for T2")
	}
	if !r.HasSwarm {
		t.Error("expected HasSwarm=true for T2")
	}
	if r.SwarmState != 3 {
		t.Errorf("swarm_state = %d, want 3", r.SwarmState)
	}
	if math.Abs(r.TemperatureC-35.0) > 0.01 {
		t.Errorf("temp = %.2f°C, want 35.00°C", r.TemperatureC)
	}
}

func TestParseAdvertisement_BatteryClamped(t *testing.T) {
	payload := buildPayload(
		modelTH2, 1, 1,
		0, 120, // battery > 100
		1, 5000, 0,
		0x7FFF, 0x7FFF, 50,
		0x7FFF, 0x7FFF, 0, 0,
	)

	r, err := parseAdvertisement("EE:FF:00:11:22:33", -50, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.BatteryPercent != 100 {
		t.Errorf("battery = %d, want 100 (clamped from 120)", r.BatteryPercent)
	}
}

func TestParseAdvertisement_MACUppercased(t *testing.T) {
	payload := buildPayload(
		modelT, 1, 1, 0, 50, 1, 5000, 0,
		0x7FFF, 0x7FFF, 0, 0x7FFF, 0x7FFF, 0, 0,
	)

	r, err := parseAdvertisement("aa:bb:cc:dd:ee:ff", -50, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.MAC != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("mac = %q, want %q", r.MAC, "AA:BB:CC:DD:EE:FF")
	}
}

func TestParseAdvertisement_NoHumidityModels(t *testing.T) {
	// Models that should NOT report humidity even if byte 14 is non-zero
	for _, model := range []byte{modelT, modelT2, modelW3, modelSubHub} {
		payload := buildPayload(
			model, 1, 1, 0, 50, 1, 5000, 0,
			0x7FFF, 0x7FFF, 75, // humidity byte = 75, but should be ignored
			0x7FFF, 0x7FFF, 0, 0,
		)

		r, err := parseAdvertisement("11:22:33:44:55:66", -50, payload)
		if err != nil {
			t.Fatalf("model %d: unexpected error: %v", model, err)
		}

		if r.HasHumidity {
			t.Errorf("model %d (%s): expected HasHumidity=false", model, r.Model)
		}
	}
}

func TestTracker(t *testing.T) {
	tr := newTracker()

	// First reading is always new
	if !tr.isNew("AA:BB:CC:DD:EE:FF", 100) {
		t.Error("first reading should be new")
	}

	// Same counter is not new
	if tr.isNew("AA:BB:CC:DD:EE:FF", 100) {
		t.Error("same counter should not be new")
	}

	// Different counter is new
	if !tr.isNew("AA:BB:CC:DD:EE:FF", 101) {
		t.Error("different counter should be new")
	}

	// Different MAC is new
	if !tr.isNew("11:22:33:44:55:66", 100) {
		t.Error("different MAC should be new")
	}

	// First discovery
	if !tr.isFirstDiscovery("AA:BB:CC:DD:EE:FF") {
		t.Error("first call should return true")
	}
	if tr.isFirstDiscovery("AA:BB:CC:DD:EE:FF") {
		t.Error("second call should return false")
	}
}
