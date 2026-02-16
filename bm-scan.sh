#!/usr/bin/env bash
#
# bm-scan.sh — Broodminder BLE advertisement scanner
#
# Scans for Broodminder BLE advertisements and displays parsed sensor data.
# Supports ALL known Broodminder device models (legacy and current).
#
# Based on:
#   - https://github.com/dstrickler/broodminder-diy (original 2018 C/Python)
#   - BroodMinder User Guide v4.50, Appendix B (official BLE packet spec)
#   - https://github.com/sandersmeenk/home_assistant-broodminder (HA integration)
#   - https://doc.mybroodminder.com/30_sensors/ (official sensor docs)
#
# Usage:
#   sudo ./bm-scan.sh                  # scan continuously
#   sudo ./bm-scan.sh -d 30            # scan for 30 seconds
#   sudo ./bm-scan.sh -j               # output as JSON
#   sudo ./bm-scan.sh -c               # show temperature in Celsius
#
# Requires: Linux with BlueZ (hcitool, hcidump)
# Must run as root (sudo) for BLE scanning privileges.
#
# Dependencies: hcitool, hcidump (bluez), bc
#   sudo apt-get install bluez bluez-tools bc

set -euo pipefail

# --- Configuration ---
DURATION=0          # 0 = continuous
CELSIUS=false
JSON_OUTPUT=false
SHOW_ALL=false
HCI_DEV="hci0"

# --- Device model constants (decimal values of byte 10 in advertisement) ---
# Legacy models (SHT-like temperature formula)
MODEL_T=41       # Temperature only (1st gen)
MODEL_TH=42      # Temperature + Humidity (1st gen)
MODEL_W=43       # Weight scale, 2 cells (1st gen)
# Current models (centigrade + 5000 offset temperature formula)
MODEL_T2=47      # Temperature + SwarmMinder (T2/T3)
MODEL_W3=49      # Weight scale, 4 cells (W3/W4)
MODEL_SUBHUB=52  # SubHub BLE relay
MODEL_HUB4G=54   # Cell Hub / Hub 4G
MODEL_TH2=56     # Temp + Humidity + SwarmMinder (TH2/TH3)
MODEL_WPLUS=57   # Weight scale, 2 cells (W+/W2)
MODEL_DIY=58     # DIY weight scale, 4 cells
MODEL_HUBWF=60   # WiFi Hub
MODEL_BEEDAR=63  # BeeDar (flight counter + acoustic)

# --- Argument parsing ---
usage() {
    cat <<'EOF'
Usage: sudo bm-scan.sh [OPTIONS]

Scan for Broodminder BLE advertisements and display sensor data.
Supports: T, TH, W, T2/T3, TH2/TH3, W+, W3/W4, DIY, SubHub, BeeDar, Hub

Options:
  -d SECONDS    Scan duration in seconds (0 = continuous, default: 0)
  -c            Show temperature in Celsius (default: Fahrenheit)
  -j            Output readings as JSON lines
  -a            Show all advertisements (don't deduplicate)
  -i DEVICE     HCI device to use (default: hci0)
  -h            Show this help

Examples:
  sudo ./bm-scan.sh              # scan continuously, Fahrenheit
  sudo ./bm-scan.sh -d 60 -c    # scan 60 seconds, Celsius
  sudo ./bm-scan.sh -j           # JSON output for piping
EOF
    exit 0
}

while getopts "d:cjai:h" opt; do
    case $opt in
        d) DURATION="$OPTARG" ;;
        c) CELSIUS=true ;;
        j) JSON_OUTPUT=true ;;
        a) SHOW_ALL=true ;;
        i) HCI_DEV="$OPTARG" ;;
        h) usage ;;
        *) usage ;;
    esac
done

# --- Dependency checks ---
for cmd in hcitool hcidump bc; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "error: '$cmd' not found. Install with:" >&2
        echo "  sudo apt-get install bluez bluez-tools bc" >&2
        exit 1
    fi
done

if [[ $EUID -ne 0 ]]; then
    echo "error: must run as root (sudo)" >&2
    exit 1
fi

# --- Globals ---
declare -A SEEN_COUNTERS   # MAC -> last sample counter (for dedup)
declare -A KNOWN_DEVICES   # MAC -> 1 (for discovery logging)
DEVICE_COUNT=0
HCIDUMP_PID=""
SCAN_PID=""
TIMER_PID=""

# --- Helper functions ---

# Convert two hex bytes (little-endian) to uint16
le16() {
    printf "%d" "0x${2}${1}"
}

# Convert two hex bytes (little-endian) to signed weight (subtract 32767, divide by 100)
le16_weight() {
    local val
    val=$(printf "%d" "0x${2}${1}")
    echo $(( val - 32767 ))
}

# Model name from decimal model byte
model_name() {
    local dec=$1
    case "$dec" in
        41) echo "T" ;;
        42) echo "TH" ;;
        43) echo "W" ;;
        47) echo "T2" ;;
        49) echo "W3" ;;
        52) echo "SubHub" ;;
        54) echo "Hub4G" ;;
        56) echo "TH2" ;;
        57) echo "W+" ;;
        58) echo "DIY" ;;
        60) echo "HubWF" ;;
        63) echo "BeeDar" ;;
        *)  echo "?($dec)" ;;
    esac
}

# Check if model uses legacy SHT-like temperature formula
is_legacy_temp() {
    local dec=$1
    [[ "$dec" == "41" || "$dec" == "42" || "$dec" == "43" ]]
}

# Check if model has valid humidity data
has_humidity() {
    local dec=$1
    # Models 41, 47, 49, 52 always report 0 for humidity
    [[ "$dec" != "41" && "$dec" != "47" && "$dec" != "49" && "$dec" != "52" ]]
}

# Check if model is a weight device
is_weight_model() {
    local dec=$1
    [[ "$dec" == "43" || "$dec" == "57" || "$dec" == "49" || "$dec" == "58" ]]
}

# Check if model has 4 load cells
is_4cell_model() {
    local dec=$1
    [[ "$dec" == "49" || "$dec" == "58" ]]
}

# Check if weight raw value is a sentinel (invalid)
is_weight_sentinel() {
    local raw=$1
    [[ "$raw" == "32767" || "$raw" == "32773" || "$raw" == "65535" ]]
}

# Parse temperature from raw uint16 value, model-aware
# Legacy models (41,42,43): (raw / 65536) * 165 - 40 = °C
# Newer models (47+): (raw - 5000) / 100 = °C
parse_temperature() {
    local model_dec=$1
    local raw=$2
    if [[ "$raw" == "65535" ]]; then
        echo "0"
        return
    fi
    if is_legacy_temp "$model_dec"; then
        echo "scale=2; ($raw / 65536.0) * 165.0 - 40.0" | bc
    else
        echo "scale=2; ($raw - 5000) / 100.0" | bc
    fi
}

# --- Main parse function ---
# Arguments: MAC RSSI byte0 byte1 byte2 ... (hex bytes starting at device model)
parse_reading() {
    local mac="$1"
    local rssi="$2"
    shift 2
    local bytes=("$@")

    if [[ ${#bytes[@]} -lt 15 ]]; then
        echo "warning: payload too short for $mac: ${#bytes[@]} bytes" >&2
        return 1
    fi

    # Model (index 0) — convert hex to decimal for model logic
    local model_hex="${bytes[0]}"
    local model_dec
    model_dec=$(printf "%d" "0x$model_hex")
    local model
    model=$(model_name "$model_dec")

    # Firmware (index 1 = minor, index 2 = major)
    local firmware
    firmware="$(printf "%d" "0x${bytes[2]}").$(printf "%02d" "0x${bytes[1]}")"

    # Battery (index 4, clamp to 100)
    local battery
    battery=$(printf "%d" "0x${bytes[4]}")
    if [[ $battery -gt 100 ]]; then battery=100; fi

    # Sample counter (little-endian uint16 at index 5-6)
    local sample
    sample=$(le16 "${bytes[5]}" "${bytes[6]}")

    # Deduplication
    if [[ "$SHOW_ALL" == "false" ]]; then
        local prev="${SEEN_COUNTERS[$mac]:-}"
        if [[ "$prev" == "$sample" ]]; then
            return 0
        fi
        SEEN_COUNTERS["$mac"]="$sample"
    fi

    # Device discovery logging
    if [[ -z "${KNOWN_DEVICES[$mac]:-}" ]]; then
        KNOWN_DEVICES["$mac"]=1
        DEVICE_COUNT=$((DEVICE_COUNT + 1))
        if [[ "$JSON_OUTPUT" == "false" ]]; then
            echo "Discovered Broodminder device #${DEVICE_COUNT}: ${mac} (${model})" >&2
        fi
    fi

    # Temperature (little-endian uint16 at index 7-8)
    local temp_raw temp_c temp_f
    temp_raw=$(le16 "${bytes[7]}" "${bytes[8]}")
    temp_c=$(parse_temperature "$model_dec" "$temp_raw")
    temp_f=$(echo "scale=1; ($temp_c * 9.0 / 5.0) + 32.0" | bc)

    # Weight (index 10-13)
    local has_weight="false"
    local weight_l="0" weight_r="0" weight_total="0"
    if is_weight_model "$model_dec" && [[ ${#bytes[@]} -ge 14 ]]; then
        local wl_raw_uint wr_raw_uint
        wl_raw_uint=$(le16 "${bytes[10]}" "${bytes[11]}")
        wr_raw_uint=$(le16 "${bytes[12]}" "${bytes[13]}")

        if ! is_weight_sentinel "$wl_raw_uint" && ! is_weight_sentinel "$wr_raw_uint"; then
            local wl_signed wr_signed
            wl_signed=$(( wl_raw_uint - 32767 ))
            wr_signed=$(( wr_raw_uint - 32767 ))
            weight_l=$(echo "scale=2; $wl_signed / 100.0" | bc)
            weight_r=$(echo "scale=2; $wr_signed / 100.0" | bc)
            weight_total=$(echo "scale=2; $weight_l + $weight_r" | bc)
            has_weight="true"
        fi
    fi

    # 4-cell weight (index 15-18) for W3/W4/DIY
    local has_4cell="false"
    local weight_l2="0" weight_r2="0"
    if is_4cell_model "$model_dec" && [[ ${#bytes[@]} -ge 19 ]]; then
        local wl2_raw_uint wr2_raw_uint
        wl2_raw_uint=$(le16 "${bytes[15]}" "${bytes[16]}")
        wr2_raw_uint=$(le16 "${bytes[17]}" "${bytes[18]}")

        if ! is_weight_sentinel "$wl2_raw_uint" && ! is_weight_sentinel "$wr2_raw_uint"; then
            local wl2_signed wr2_signed
            wl2_signed=$(( wl2_raw_uint - 32767 ))
            wr2_signed=$(( wr2_raw_uint - 32767 ))
            weight_l2=$(echo "scale=2; $wl2_signed / 100.0" | bc)
            weight_r2=$(echo "scale=2; $wr2_signed / 100.0" | bc)
            weight_total=$(echo "scale=2; $weight_l + $weight_r + $weight_l2 + $weight_r2" | bc)
            has_4cell="true"
        fi
    fi

    # Humidity (index 14)
    local humidity=0
    local show_humidity="false"
    if has_humidity "$model_dec" && [[ ${#bytes[@]} -ge 15 ]]; then
        humidity=$(printf "%d" "0x${bytes[14]}")
        if [[ $humidity -ge 0 && $humidity -le 100 ]]; then
            show_humidity="true"
        fi
    fi

    local ts
    ts=$(date +%H:%M:%S)

    # --- Output ---
    if [[ "$JSON_OUTPUT" == "true" ]]; then
        local json_weight="" json_4cell="" json_humidity=""
        if [[ "$has_weight" == "true" ]]; then
            json_weight="\"weight_left\":${weight_l},\"weight_right\":${weight_r},\"weight_total\":${weight_total},"
            if [[ "$has_4cell" == "true" ]]; then
                json_4cell="\"weight_left_2\":${weight_l2},\"weight_right_2\":${weight_r2},\"has_4cell\":true,"
            fi
        fi
        if [[ "$show_humidity" == "true" ]]; then
            json_humidity="\"humidity_pct\":${humidity},\"has_humidity\":true,"
        fi
        printf '{"mac":"%s","rssi":%s,"model":"%s","model_byte":%d,"firmware":"%s","battery_percent":%d,"sample_counter":%d,"temperature_c":%s,"temperature_f":%s,%s%s%s"has_weight":%s,"timestamp":"%s"}\n' \
            "$mac" "$rssi" "$model" "$model_dec" "$firmware" "$battery" "$sample" \
            "$temp_c" "$temp_f" "$json_humidity" "$json_weight" "$json_4cell" \
            "$has_weight" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    else
        local temp_display
        if [[ "$CELSIUS" == "true" ]]; then
            temp_display="${temp_c}°C"
        else
            temp_display="${temp_f}°F"
        fi

        local line
        line=$(printf "[%s] %s %-6s FW:%s  Bat:%3d%%  Sample:%5d  Temp:%s" \
            "$ts" "$mac" "$model" "$firmware" "$battery" "$sample" "$temp_display")

        if [[ "$show_humidity" == "true" ]]; then
            line+=$(printf "  Humidity:%3d%%" "$humidity")
        fi

        if [[ "$has_weight" == "true" ]]; then
            line+=$(printf "  Wt: L=%s R=%s" "$weight_l" "$weight_r")
            if [[ "$has_4cell" == "true" ]]; then
                line+=$(printf " L2=%s R2=%s" "$weight_l2" "$weight_r2")
            fi
            line+=$(printf " Total=%s kg" "$weight_total")
        fi

        echo "$line"
    fi
}

# --- Cleanup ---
cleanup() {
    [[ -n "$HCIDUMP_PID" ]] && kill "$HCIDUMP_PID" 2>/dev/null || true
    [[ -n "$SCAN_PID" ]] && kill "$SCAN_PID" 2>/dev/null || true
    [[ -n "$TIMER_PID" ]] && kill "$TIMER_PID" 2>/dev/null || true

    # Disable LE scan
    hcitool -i "$HCI_DEV" cmd 0x08 0x000C 00 00 >/dev/null 2>&1 || true

    if [[ "$JSON_OUTPUT" == "false" ]]; then
        echo "" >&2
        echo "---" >&2
        echo "Scan complete. Found ${DEVICE_COUNT} Broodminder device(s)." >&2
    fi
}
trap cleanup EXIT

# --- Main ---

# Reset HCI device to a clean state
hciconfig "$HCI_DEV" down 2>/dev/null || true
sleep 0.5
hciconfig "$HCI_DEV" up 2>/dev/null || {
    echo "error: failed to bring up $HCI_DEV" >&2
    echo "hint: check that Bluetooth hardware is available" >&2
    exit 1
}

if [[ "$JSON_OUTPUT" == "false" ]]; then
    echo "Scanning for Broodminder BLE devices..." >&2
    echo "Supported: T, TH, W, T2/T3, TH2/TH3, W+, W3/W4, DIY, SubHub, BeeDar, Hub" >&2
    if [[ "$DURATION" -gt 0 ]]; then
        echo "Duration: ${DURATION}s" >&2
    else
        echo "Press Ctrl+C to stop" >&2
    fi
    echo "---" >&2
fi

# Enable passive LE scanning via HCI commands
# Set scan parameters: passive scan, 10ms interval, 10ms window
hcitool -i "$HCI_DEV" cmd 0x08 0x000B 00 10 00 10 00 00 00 >/dev/null 2>&1
# Enable scanning, allow duplicates
hcitool -i "$HCI_DEV" cmd 0x08 0x000C 01 00 >/dev/null 2>&1

# Start hcitool lescan in background to keep scanning active
hcitool -i "$HCI_DEV" lescan --passive --duplicates >/dev/null 2>&1 &
SCAN_PID=$!

# Duration timer
if [[ "$DURATION" -gt 0 ]]; then
    ( sleep "$DURATION" && kill -TERM $$ 2>/dev/null ) &
    TIMER_PID=$!
fi

# Process raw HCI packets from hcidump
#
# hcidump --raw outputs lines like:
#   > 04 3E 2B 02 01 00 01 07 30 B5 80 07 00 1F 02 01 06 ...
#   (continuation lines start with whitespace)
#
# We accumulate multi-line packets, find the manufacturer-specific data
# marker (18 FF) followed by BroodMinder ID (8D 02), then parse.

current_packet=""

hcidump -i "$HCI_DEV" --raw 2>/dev/null | while IFS= read -r line; do
    if [[ "$line" == ">"* ]]; then
        # Process the previously accumulated packet
        if [[ -n "$current_packet" ]]; then
            # Split into byte array
            # shellcheck disable=SC2206
            pkt_bytes=($current_packet)
            pkt_len=${#pkt_bytes[@]}

            # Scan for 18 FF 8D 02 (manufacturer-specific data, BroodMinder ID)
            for (( i=0; i < pkt_len - 3; i++ )); do
                if [[ "${pkt_bytes[$i]^^}" == "18" && \
                      "${pkt_bytes[$((i+1))]^^}" == "FF" && \
                      "${pkt_bytes[$((i+2))]^^}" == "8D" && \
                      "${pkt_bytes[$((i+3))]^^}" == "02" ]]; then

                    # Extract MAC address from HCI LE Advertising Report header
                    # Standard layout: 04 3E <len> 02 01 <type> <MAC[6] reversed> <ad_len> ...
                    # MAC bytes are at offsets 7-12 (reversed)
                    mac="UNKNOWN"
                    if [[ $pkt_len -gt 12 ]]; then
                        mac="${pkt_bytes[12]^^}:${pkt_bytes[11]^^}:${pkt_bytes[10]^^}:${pkt_bytes[9]^^}:${pkt_bytes[8]^^}:${pkt_bytes[7]^^}"
                    fi

                    # RSSI is the last byte (signed)
                    rssi_hex="${pkt_bytes[$((pkt_len - 1))]}"
                    rssi_val=$(printf "%d" "0x$rssi_hex")
                    if [[ $rssi_val -gt 127 ]]; then
                        rssi_val=$(( rssi_val - 256 ))
                    fi

                    # Payload starts after 8D 02
                    payload_start=$(( i + 4 ))
                    payload=()
                    for (( j=payload_start; j < pkt_len - 1; j++ )); do
                        payload+=("${pkt_bytes[$j],,}")
                    done

                    if [[ ${#payload[@]} -ge 15 ]]; then
                        parse_reading "$mac" "$rssi_val" "${payload[@]}" || true
                    fi
                    break
                fi
            done
        fi

        # Start new packet (strip "> " prefix)
        current_packet="${line#> }"
    else
        # Continuation line — append (strip leading whitespace)
        current_packet="$current_packet ${line#"${line%%[![:space:]]*}"}"
    fi
done
