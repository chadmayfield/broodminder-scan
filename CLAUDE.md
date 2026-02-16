# broodminder-scan

BLE scanner for Broodminder beehive sensors. Go and shell implementations supporting all 12 known device models.

## Build & Test

- **Build:** `go build -o bm-scan .`
- **Test:** `go test -race ./...`
- **Vet:** `go vet ./...`
- **Cross-compile (Pi 64-bit):** `GOOS=linux GOARCH=arm64 go build -o bm-scan-linux-arm64 .`
- **Cross-compile (Pi 32-bit):** `GOOS=linux GOARCH=arm GOARM=7 go build -o bm-scan-linux-arm .`

## Conventions

- **Single-binary repo.** All Go code lives in `main.go` and `main_test.go`. No packages, no subdirectories.
- **Binary name:** `bm-scan` (short for CLI usage). Repo name is `broodminder-scan`.
- **Two temperature formulas.** Legacy models (41, 42, 43) use SHT-like: `(raw/65536)*165-40`. Current models (47+) use centigrade: `(raw-5000)/100`. Always check `legacyTempModels` map.
- **Weight sentinel values.** Raw values 0x7FFF, 0x8005, 0xFFFF are invalid — skip them.
- **All internal values in metric.** Temperature in °C, weight in kg. Fahrenheit/pounds are display-only conversions.
- **BLE dependency.** `tinygo.org/x/bluetooth` is the only external dependency. No CGO required.
- **Version injection.** Set at build time via `-ldflags "-X main.version=vX.Y.Z"`. CI does this on tagged releases.
- **No secrets.** This tool reads BLE advertisements passively. No API keys, no credentials, no config files.

## Working Style

- Table-driven tests for parser functions. Each device model should have at least one test case with a real or realistic packet.
- Run `go test -race ./...` before committing.
- Run `go vet ./...` before committing.
- NEVER tag/release without building and verifying the binary works.
- Keep it simple — this is a single-purpose scanning tool, not a framework.
