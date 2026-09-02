# Changelog

All notable changes to JAVBeacon Subtitles are documented here.

## [Unreleased]

## [0.4.0] - 2026-09-02

### Fixed

- Fixed oversized translated English subtitle cues (observed on ADN-803, e.g. a 28.42s/four-line block) surviving normalization unsplit whenever a cue had no trustworthy Qwen forced-alignment anchors. The normalizer now falls back to proportional redistribution of the existing timing envelope by visible-character weight as a last-resort timing source, still capped to what the source duration can hold at the configured minimum cue duration. SRT and ASS continue to be serialized from the same normalized cue list.

### Added

- Added a per-job and default `write_ass` option (web UI, JSON/multipart API, and `output.write_ass` configuration) to export `.srt` subtitles only, skipping `.en.ass`/`.ja.ass` generation.

## [0.3.0] - 2026-09-02

### Added

- Added application version reporting in the header and health response, version-injected container builds, semver GHCR tags, and tag-driven GitHub releases.
- Added strict player-compatible SRT output plus SHA-256 provenance in the existing subtitle project/job metadata and portable `.srt.json` sidecars.
- Added shared post-translation subtitle normalization that wraps text and splits oversized cues only at real forced-alignment timing anchors.
- Added ADN-803 regression coverage, silence-valley VAD splitting, energy-based short-vocalization timing recovery, separate timing-quality states, lexical-aware Reazon eligibility, fallback-benefit metrics, and optional translation cost normalization.
- Added canonical Standard/JAV/GIGA profiles, independent per-file path-based profile/accuracy resolution, persisted resolution sources, and legacy alias normalization to GIGA.
- Added Japanese Recognition Vocabulary v1 and hierarchical Translation Glossary v2 validation, scope activation, title override support, persistent import/export controls, and bundled production catalogs.
- Added prompt-leak diagnostics and one no-context Qwen retry, alignment text-integrity metrics/recovery, grouped Whisper fallback, and expanded ASR timing/fallback observability.
- Added server-side Activity pagination, case-insensitive wildcard filtering, page selection, and streaming multi-job ZIP exports with collision-safe directories, warnings, and a comparison-friendly manifest.
- Added a Models tab backed by a centralized four-role registry with installed state, exact active revisions, VRAM guidance, and manual provider update checks.
- Added a Japanese-forced Qwen3-ASR-1.7B primary pipeline with a separate Qwen3-ForcedAligner-0.6B timing phase.
- Added Fast, Balanced, and High Accuracy per-job modes in the web/API workflow.
- Added recall-biased configurable dialogue detection, padded 30-second regions, ambiguous-vocalization classification, transcript suspicion heuristics, normalized multi-ASR comparison, confidence/review states, and per-segment failure isolation.
- Added English/Japanese ASS output and a JSON subtitle project containing canonical Japanese/English tracks, model identities, processing metrics, confidence, and optional raw candidate diagnostics.
- Added a JAVBeacon-derived subtitle/translation logo, favicon set, and embedded asset route.
- Added first-run username/password web login with SQLite-backed password hashes and HTTP-only sessions, while retaining bearer-token authentication for external API clients.
- Added a dedicated Settings tab for translation and persistent Bash/webhook post-processing.
- Added an in-app Setup tab with recursive folder API, arbitrary mount, and post-processing examples.
- Included Bash and curl in the runtime image and added a configurable read-only scripts mount.
- Added scene-aware translation windows with a configurable silence boundary, compact request rows, relevant structured glossary filtering, and optional job-scoped exact translation memory.
- Added contextual translation observability for translated, context, reused, and included-glossary rows while preserving provider token totals.
- Added validated local text-file import for effectively unbounded `Japanese=English` mappings, with blank-line removal and duplicate/conflict handling.
- Added an optional high-confidence Japanese-to-English base glossary for JAV, GIGA/tokusatsu heroine, and Akiba-web-style adult/action vocabulary.
- Added a per-job `keep_japanese` web/API option for retaining `.ja.srt` beside the English subtitle.
- Added bounded overlapping transcription windows, per-window retry/progress, full-duration coverage checks, and timestamp safety validation.

### Changed

- Isolated Qwen primary and retry inference in one terminating child process so PyTorch cannot retain its ~3.3 GB CUDA reservation before Whisper, enabling full Large-v3 CUDA on the tested 10 GB NVIDIA environment (`qwen-first-v2.5`).
- Made Whisper CPU threads, beam size, and best-of configurable; selected 12 CPU threads from focused 4/8/12/16-thread testing while retaining quality-preserving beam 5/best-of 5 defaults.
- Added a configurable 4096 MB free-VRAM preflight for full Large-v3 so known-insufficient states bypass a guaranteed CUDA OOM and enter the existing CPU safety path directly.
- Redesigned Balanced recognition as Qwen → targeted no-context Qwen retry → batched Whisper Large-v3 verification; Reazon is excluded from normal Qwen production jobs and remains only as an experimental standalone compatibility backend.
- Promoted Whisper to the CUDA-capable Balanced fallback role, added deterministic candidate validation/scoring and hard 30-second fallback chunking, and advanced diagnostics to `qwen-first-v2.2`.
- Replaced verbose English Qwen profile prompts with minimal Japanese-only hints and made `standard`, `jav`, and `giga` the only persisted profile values.
- Made selected ASR text canonical: forced alignment now supplies timing and cannot silently truncate or replace Japanese dialogue.
- Tightened Balanced/High Accuracy fallback eligibility so empty candidates and minor disagreement cannot trigger Whisper.
- Changed GPU model lifecycle handling so Qwen ASR, fallback ASR, and forced alignment are released between phases instead of remaining resident together.
- Replaced the purple interface with JAVBeacon's dark red, charcoal, ivory, and Inter/system-font visual identity.
- Cached Qwen ASR and aligner snapshots on the persistent models mount and reused unchanged revisions across Docker/GHCR rebuilds.
- Removed the API-token field from the browser workflow; signed-in users have full web functionality through their session.
- Clarified that server path jobs accept any path visible to the process or container, not only `/media`.
- Reduced contextual request overhead without adding model passes, embedding calls, summaries, or more than four external context rows.
- Indexed large structured glossaries once per job and raised the settings payload allowance for very large mapping collections.
- Changed Docker startup to run the service as the configured `PUID` and `PGID` (`GID`/`GUID` aliases supported) after root-only model preparation.

### Fixed

- Added PyTorch allocated/reserved/peak diagnostics, idle-context and post-exit cleanup deltas, debug-only surviving CUDA-tensor metadata, Whisper model metadata, persisted selected-correction evidence, and fallback value metrics grouped by reason.
- Hardened the Qwen → Whisper → ForcedAligner GPU lifecycle with explicit model deletion/cache collection and staged VRAM/process diagnostics; classified CUDA OOM separately and retries the complete selective Whisper batch once on CPU when enabled (`qwen-first-v2.4`).
- Added configurable Whisper `auto`/`cuda`/`cpu` policy and CPU timeout, plus concise model path/type/quantization/size, preferred-device, last-load, CUDA-failure, and fallback availability metadata in Models.
- Fixed swallowed Whisper fallback failures by validating the model and per-candidate PCM WAVs, preserving bounded process diagnostics, explicitly reporting execution/timeout/parser failures, and mapping every multi-file result back to its source segment.
- Restored clean no-context Qwen prompt-leak retries even when the corrected utterance is much shorter, prevented unresolved prompt leakage alone from flooding Whisper, and made High Accuracy verification broader than Balanced while retaining vocalization/timing exclusions (`qwen-first-v2.3`).
- Report existing-subtitle skips honestly instead of presenting an instant no-op as a successful Fast/Balanced rerun, and expose every previously generated artifact on the skipped result.
- Enforced `max_segment_seconds` as a hard VAD-region limit so a quiet-valley split can never make Qwen exceed the 30-second validation boundary and cascade into whole-file fallbacks.
- Prevented punctuation-only ASR output from becoming subtitles or translation requests, bounded pathological long timing for tiny vocalizations, and stopped Balanced mode spending fallback work on repeated short replies and vocal reactions.
- Isolated Qwen-ASR and NeMo/ReazonSpeech in separate Python environments because their required Transformers versions conflict, while retaining one shared PyTorch/CUDA runtime to avoid duplicating the largest image dependencies.
- Installed the native SoX executable plus NumPy and `typing-extensions` before resolving Qwen/NeMo dependencies, covering the Python `sox` package's undeclared metadata-time imports during Docker builds.
- Made large Japanese term-mapping imports open at the first entry in a taller editor, with controls to show every mapping or collapse the overview.
- Made generated SRT files group-readable/writable so the configured host user and group can manage them normally.
- Prevented malformed ASR output such as a segment spanning from six minutes to the end of a feature-length recording from being accepted as a successful job.

## [0.2.1] - 2026-09-01

### Fixed

- Limited CUDA compilation parallelism and architecture fan-out so GHCR builds do not exhaust GitHub-hosted runner resources.
- Made CUDA architecture targets and compiler worker count configurable through Compose build arguments.
- Removed Whisper source and compiler intermediates from the final runtime image.

## [0.2.0] - 2026-09-01

### Added

- Added Qwen-retry and Whisper correction/benefit metrics, explicit fallback-chain metadata, candidate rejection reasons, and fallback-audio duration/RMS/peak/nonzero/source-offset diagnostics.
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
