# Changelog

All notable changes to JAVBeacon Subtitles are documented here.

## [Unreleased]

### Added

- Added first-run username/password web login with SQLite-backed password hashes and HTTP-only sessions, while retaining bearer-token authentication for external API clients.
- Added a dedicated Settings tab for translation and persistent Bash/webhook post-processing.
- Added an in-app Setup tab with recursive folder API, arbitrary mount, and post-processing examples.
- Included Bash and curl in the runtime image and added a configurable read-only scripts mount.
- Added scene-aware translation windows with a configurable silence boundary, compact request rows, relevant structured glossary filtering, and optional job-scoped exact translation memory.
- Added contextual translation observability for translated, context, reused, and included-glossary rows while preserving provider token totals.

### Changed

- Removed the API-token field from the browser workflow; signed-in users have full web functionality through their session.
- Clarified that server path jobs accept any path visible to the process or container, not only `/media`.
- Reduced contextual request overhead without adding model passes, embedding calls, summaries, or more than four external context rows.

## [0.2.1] - 2026-09-01

### Fixed

- Limited CUDA compilation parallelism and architecture fan-out so GHCR builds do not exhaust GitHub-hosted runner resources.
- Made CUDA architecture targets and compiler worker count configurable through Compose build arguments.
- Removed Whisper source and compiler intermediates from the final runtime image.

## [0.2.0] - 2026-09-01

### Added

- Added permanent SQLite-backed translation settings to the web interface and REST API, with write-only API-key handling.
- Added current path, filename, file number, live Whisper progress, and ETA to activity jobs.
- Added automatic CPU fallback when CUDA preflight or inference fails.

### Changed

- Removed application-level path restrictions; any container-visible or locally readable media path can now be submitted.
- Replaced the moving upstream CUDA image with a pinned whisper.cpp source build compiled with CUDA VMM disabled.
- Disabled automatic GPU reset by default because primary/display GPUs cannot normally be reset safely.

## [0.1.5] - 2026-08-30

### Fixed

- Fixed single-file submission in browsers where the built-in `window.external` object shadowed the JAVBeacon reference input.
- Improved API-token feedback and automatically refresh health and activity after the browser token changes.

## [0.1.4] - 2026-08-30

### Changed

- Made the GHCR publishing workflow run automatically on every push to `main`, attaching build progress directly to the pushed commit while retaining manual dispatch as a fallback.

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
