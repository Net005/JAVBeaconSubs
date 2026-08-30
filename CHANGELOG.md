# Changelog

All notable changes to JAVBeacon Subtitles are documented here.

## [0.1.3] - 2026-08-30

### Changed

- Standardized all internal identifiers on `javbeaconsubs`, including the Go module, command, binary, environment variables, Compose service, database, and GHCR image.
- Replaced the named data volume and fixed model bind mount with configurable `JAVBEACONSUBS_DATA_PATH` and `JAVBEACONSUBS_MODELS_PATH` host paths.
- Made `SUBTITLE_PORT` control the application listener, published container port, and health check together.
- Documented the distinct purposes of `JAVBEACONSUBS_API_TOKEN` and `JAVBEACONSUBS_TRANSLATION_API_KEY`.

## [0.1.2] - 2026-08-30

### Changed

- Generalized the NVIDIA CUDA documentation and removed GPU model-specific wording.

## [0.1.1] - 2026-08-30

### Changed

- Made the primary Docker Compose stack NVIDIA/CUDA-first with an explicit, configurable GPU device reservation.
- Made the GHCR image use whisper.cpp's CUDA runtime by default and added a standalone CPU Compose fallback.

## [0.1.0] - 2026-08-30

### Added

- Go subtitle service optimized for Japanese-to-English transcription and translation.
- Single-file browser uploads and explicit file or folder jobs through the REST API.
- JAVBeacon correlation IDs, callbacks, server-sent job events, and persistent SQLite job history.
- Direct Whisper translation, contextual OpenAI-compatible translation, glossary support, and token accounting.
- Docker Compose configurations for CPU, NVIDIA GPU, and JAVBeacon network integration.
- Persistent, checksum-verified `ggml-large-v3` and Silero VAD model caching outside the Docker build context.
- CUDA preflight checks, guarded GPU reset attempts, inference retry, and per-file process cleanup.
- Manual GitHub Actions workflow for publishing version, commit, and `latest` images to GHCR.

### Improved

- Japanese speech detection through fixed-language recognition, padded VAD, audio normalization, and conservative overlap deduplication.
- Subtitle readability through atomic SRT output and constrained line formatting.
