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
  if [[ "${JAVBEACONSUBS_DOWNLOAD_MODELS:-true}" != "true" ]]; then
    echo "Missing model: $destination" >&2
    echo "Mount the model or set JAVBEACONSUBS_DOWNLOAD_MODELS=true." >&2
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

download_model "${JAVBEACONSUBS_WHISPER_MODEL:-/models/ggml-large-v3.bin}" \
  "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3.bin?download=true" \
  "ad82bf6a9043ceed055076d0fd39f5f186ff8062"
download_model "${JAVBEACONSUBS_VAD_MODEL:-/models/ggml-silero-v6.2.0.bin}" \
  "https://huggingface.co/ggml-org/whisper-vad/resolve/main/ggml-silero-v6.2.0.bin?download=true"

if [[ "${JAVBEACONSUBS_ASR_BACKEND:-qwen}" == "qwen" ]]; then
  qwen_marker="${JAVBEACONSUBS_QWEN_READY_MARKER:-/models/huggingface/.javbeaconsubs-qwen-ready.json}"
  qwen_model="${JAVBEACONSUBS_QWEN_MODEL:-Qwen/Qwen3-ASR-1.7B}"
  qwen_revision="${JAVBEACONSUBS_QWEN_REVISION:-7278e1e70fe206f11671096ffdd38061171dd6e5}"
  aligner_model="${JAVBEACONSUBS_ALIGNER_MODEL:-Qwen/Qwen3-ForcedAligner-0.6B}"
  aligner_revision="${JAVBEACONSUBS_ALIGNER_REVISION:-c7cbfc2048c462b0d63a45797104fc9db3ad62b7}"
  if [[ ! -s "$qwen_marker" ]] || ! grep -Fq "$qwen_model" "$qwen_marker" || ! grep -Fq "$qwen_revision" "$qwen_marker" || ! grep -Fq "$aligner_model" "$qwen_marker" || ! grep -Fq "$aligner_revision" "$qwen_marker"; then
    if [[ "${JAVBEACONSUBS_DOWNLOAD_MODELS:-true}" != "true" ]]; then
      echo "Missing cached Qwen ASR and forced-alignment models." >&2
      exit 1
    fi
    echo "Caching Qwen Japanese ASR and forced-alignment models..."
    python3 "${JAVBEACONSUBS_QWEN_SCRIPT:-/app/asr/qwen_pipeline.py}" \
      --download-only \
      --qwen-model "$qwen_model" \
      --qwen-revision "$qwen_revision" \
      --aligner-model "$aligner_model" \
      --aligner-revision "$aligner_revision" \
      --ready-marker "$qwen_marker"
  fi
fi

if [[ "${JAVBEACONSUBS_ASR_BACKEND:-qwen}" == "reazon" ]] || [[ "${JAVBEACONSUBS_REAZON_ENABLED:-true}" == "true" ]]; then
  reazon_model="${JAVBEACONSUBS_REAZON_MODEL:-reazon-research/reazonspeech-nemo-v2}"
  reazon_marker="${JAVBEACONSUBS_REAZON_READY_MARKER:-/models/reazonspeech/.javbeaconsubs-ready}"
  if [[ ! -f "$reazon_marker" ]] || [[ "$(<"$reazon_marker")" != "$reazon_model" ]]; then
    if [[ "${JAVBEACONSUBS_DOWNLOAD_MODELS:-true}" != "true" ]]; then
      echo "Missing cached ReazonSpeech model: $reazon_model" >&2
      exit 1
    fi
    echo "Caching ReazonSpeech model $reazon_model..."
    python3 "${JAVBEACONSUBS_REAZON_SCRIPT:-/app/asr/reazon_worker.py}" \
      --download-only --device cpu --model "$reazon_model" --ready-marker "$reazon_marker"
  fi
fi

puid="${PUID:-1000}"
pgid="${PGID:-${GID:-${GUID:-1000}}}"
if [[ ! "$puid" =~ ^[0-9]+$ ]] || [[ ! "$pgid" =~ ^[0-9]+$ ]]; then
  echo "PUID and PGID/GID/GUID must be numeric user and group IDs." >&2
  exit 1
fi

# Models are prepared as root, then the long-running service drops privileges.
# Application data and the writable Hugging Face cache are re-owned; media mounts
# must already permit this UID/GID.
mkdir -p /data/uploads
reazon_cache="${HF_HOME:-/models/huggingface}"
mkdir -p "$reazon_cache"
chown -R "$puid:$pgid" /data "$reazon_cache"
umask "${UMASK:-0002}"
exec setpriv --reuid="$puid" --regid="$pgid" --clear-groups "$@"
