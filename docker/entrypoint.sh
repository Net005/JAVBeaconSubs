#!/usr/bin/env bash
set -euo pipefail

download_model() {
  local destination="$1"
  local url="$2"
  local expected_sha1="${3:-}"
  local verified_marker="${destination}.verified-sha1"
  if [[ -s "$destination" ]]; then
    if [[ -z "$expected_sha1" ]]; then
      return
    fi
    if [[ -f "$verified_marker" ]] && [[ "$(<"$verified_marker")" == "$expected_sha1" ]]; then
      return
    fi
    echo "Verifying cached $(basename "$destination")..."
    if [[ "$(sha1sum "$destination" | awk '{print $1}')" == "$expected_sha1" ]]; then
      printf '%s' "$expected_sha1" > "$verified_marker"
      return
    fi
    echo "Cached $(basename "$destination") does not match the pinned revision; replacing it."
  fi
  if [[ "${BEACONSUBS_DOWNLOAD_MODELS:-true}" != "true" ]]; then
    echo "Missing model: $destination" >&2
    echo "Mount the model or set BEACONSUBS_DOWNLOAD_MODELS=true." >&2
    exit 1
  fi
  echo "Downloading $(basename "$destination"); the first startup can take several minutes..."
  mkdir -p "$(dirname "$destination")"
  curl --fail --location --retry 5 --retry-delay 3 --output "${destination}.part" "$url"
  if [[ -n "$expected_sha1" ]]; then
    local actual_sha1
    actual_sha1="$(sha1sum "${destination}.part" | awk '{print $1}')"
    if [[ "$actual_sha1" != "$expected_sha1" ]]; then
      rm -f "${destination}.part"
      echo "Checksum mismatch for $(basename "$destination"): expected $expected_sha1, got $actual_sha1" >&2
      exit 1
    fi
  fi
  mv "${destination}.part" "$destination"
  if [[ -n "$expected_sha1" ]]; then
    printf '%s' "$expected_sha1" > "$verified_marker"
  fi
}

download_model "${BEACONSUBS_WHISPER_MODEL:-/models/ggml-large-v3.bin}" \
  "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3.bin?download=true" \
  "ad82bf6a9043ceed055076d0fd39f5f186ff8062"
download_model "${BEACONSUBS_VAD_MODEL:-/models/ggml-silero-v6.2.0.bin}" \
  "https://huggingface.co/ggml-org/whisper-vad/resolve/main/ggml-silero-v6.2.0.bin?download=true"

exec "$@"
