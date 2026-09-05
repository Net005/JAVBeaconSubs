# Changelog

All notable changes to JAVBeacon Subtitles are documented here.

## [Unreleased]

### Added

- Replaced Activity's raw internal profile/mode mapping text with a responsive visual metadata strip for total files, completed files, recognition accuracy, and content profile. Internal values such as `high_accuracy` and `path_mapping` are presented as friendly labels such as **High Accuracy** and **Matched: Path rule** with distinct visual indicators. Multi-file jobs also show their common scanned path, while completed jobs no longer duplicate the last processed filename/path above the per-file output cards.
- Grouped every generated subtitle and diagnostic download by its source media file in Activity. Multi-file jobs now show filename, full path, per-file position, and that file's artifacts in a full-width bounded scroll area with a clear high-contrast scrollbar, designed to stay usable for batches of 50 or more files.
- Added opt-in JAVBeacon release auto-detection to JSON and multipart job APIs and the web job form. Each media file is matched case-insensitively by exact `stash_file_path`, filename stem as `video_id`, then filename stem with hyphens removed as `video_id`; explicit release IDs still take precedence and ambiguous matches are rejected.
- Expanded the JAVBeacon settings test area to accept exactly one release ID, full file path, video ID (including lower-case values such as `ssis-001`), or filename. Tests clear stale output before every request, show the winning lookup rule and returned Stash path, and include a dedicated Clear results action.
- Updated the in-app Setup examples and README API contract for `auto_detect_release`, per-file folder resolution, and JAVBeacon v1.0.71's exact `stash_file_path` query.

## [0.11.0] - 2026-09-04

### Added

- Added JAVBeacon API settings to the web UI: a new "JAVBeacon" settings card lets an operator set/update the API base URL, API key (write-only - the response only ever reports whether one is saved), and request timeout, persisted the same way as the other settings sections. Runtime updates take effect immediately for every job created afterward (the lookup client is rebuilt in place; in-flight jobs are unaffected).
- Added a "Test connection" action and a "Test a release ID" lookup to the same card: both call a new `POST /api/v1/javbeacon/test` endpoint against the form's current (not-yet-saved) values, falling back to the saved base URL/API key/timeout for any field left blank. A release ID test shows the matched release's title, story, video ID, source, and StashApp match status directly in the UI; a plain connection test just confirms the server is reachable. Every outcome (matched, not found, unreachable) renders inline - nothing needs to be saved first to try it.

### Notes

- Translation-only features, recognition, the subtitle normalizer, and provenance hashing are unchanged; this is UI/API surface for the existing JAVBeacon release-lookup client (`internal/release`), not a new lookup capability.

## [0.10.0] - 2026-09-04

### Added

- Added job-local terminology collection (Stage 4 of the post-0.6.0 combined TODO, revisiting Stage 2/3's deferred items): for each translated file, the terms already established for it - structured-glossary/`title_or_series_overrides` entries whose Japanese source actually appears in the file's dialogue, plus every accepted translation-memory form - are now collected into a single deduplicated list and passed to the mixed-script repair pass as one more optional "Known proper names/terms already established for this file" system-prompt block. Deliberately built only from already-structured data with zero free-text extraction, and deliberately not split into semantic categories (character/scenario/organization/series); no new config flag or API field, since this is internal prompt context exactly like the existing glossary/release-context blocks.
- Added proper-name variant flagging: a new diagnostic-only pass (`internal/engine/qa.go`) scans translated output for single-word capitalized candidates that are not an exact match to any job-local term but are suspiciously close (Levenshtein distance 1-2, both sides at least 5 characters) to exactly one of them - e.g. catching "Moonligt" as a likely misspelling of an established "Moonlight". Catalog codes (`ADN-803`) and acronyms (`MVSD`) are excluded by construction. Never auto-corrects anything - flags only. New `translation_proper_name_variants_detected` count and `translation_proper_name_variants` (row index/candidate/likely-intended/edit-distance detail per flagged term) diagnostics on the job result. New `proper_name_variant_qa_enabled` config flag (default `true`) disables the pass entirely.
- Added per-file release metadata for batch jobs: the JSON "paths" job API accepts an optional `file_release_overrides` map (keyed by the same path used in `inputs`) giving one or more files in a batch their own `release_external_id`/`javbeacon_release_id`/`release_title`/`release_story`, resolved through the same JAVBeacon/manual lookup path as the job-level fields. A file with no override entry keeps using the job-level release metadata, mirroring the existing per-file ASR `file_settings` fallback pattern. Overridden files' resolved metadata is exposed on the job as `file_release_metadata`.
- The web UI's job card Release summary now shows an expandable story preview and the metadata source (manual vs. JAVBeacon) alongside the existing title/provider/lookup-method line; a new per-file line lists each overridden file's resolved title, JAVBeacon ID, and lookup method when a batch job used `file_release_overrides`.

### Notes

- Translation-only: recognition (Qwen/Whisper/ForcedAligner/VAD), the subtitle normalizer, provenance hashing, and the prompt-leak vocalization filter are unchanged.
- Provenance sidecar fields (release metadata added to the `.provenance` JSON alongside the existing hash/version/backend fields) remain out of scope this stage: the information is already available via the job API response and each file's `project_json` sidecar, and adding it would require new cross-package plumbing (`metadata_provider`/`release_lookup_method` do not currently reach the `engine` package) touching the provenance-hashing system this project treats as protected. A small, well-scoped follow-up if wanted.
- This is Stage 4, revisiting items deferred from Stages 2 and 3 of the post-0.6.0 combined TODO.

## [0.9.0] - 2026-09-04

### Added

- Added a post-translation mixed-script QA pass (Stage 3 of the post-0.6.0 combined TODO): after contextual translation, every row is scanned for leftover Japanese script (hiragana/katakana/kanji) that leaked into the English output. Only the flagged rows - never the whole file - are sent through one selective re-translation batch with a stricter, script-forbidding system prompt (same glossary/release-context composition as the main prompt, so repaired rows stay terminology-consistent). A repair that still contains script, errors, or omits a row leaves the original translated text untouched rather than risk corrupting output. New `mixed_script_qa_enabled` config flag (`translation.mixed_script_qa_enabled`, default `true`) disables the pass entirely.
- Added `translation_rows_with_japanese_script`, `translation_rows_retranslated_for_validation`, and `translation_retranslation_success` diagnostics to the job result.
- Added a canonical text/time density QA check: the final (post-normalization) Japanese cues are scanned for characters-per-second density beyond a configurable `subtitle_extreme_cps` threshold (default `40`). This catches a specific gap in the existing normalizer - a cue can be short enough in total duration and character count to never be considered for splitting, yet still pack an unreadable number of characters into a very brief window (e.g. ~50+ characters in ~1 second) - without ever inventing timing, splitting cues, or rewriting canonical text; it is purely a diagnostic flag for review. New `canonical_rows_over_extreme_cps` count and `canonical_density_anomalies` (index/timing/character-density detail per flagged cue) diagnostics on the job result; set `subtitle_extreme_cps` to `0` to disable.
- Job cards in the web UI now show a compact "Translation QA" summary line (script-leakage rows flagged/fixed, extreme-density rows flagged) aggregated across a job's files, only rendered when there is something to report.

### Notes

- Translation-only: recognition (Qwen/Whisper/ForcedAligner/VAD), the subtitle normalizer's splitting/timing architecture, provenance hashing, and the prompt-leak vocalization filter are unchanged. Density QA never alters segment timing or text - detection only.
- Proper-name variant consistency (e.g. "Spandexer/Spadeksa"-style drift) and a job-local terminology memory structure remain explicitly out of scope, for the same fabrication-risk reasons Stage 2 deferred a similar item: reliable detection needs either fuzzy-matching heuristics this project deliberately avoids or a curated per-file reference list that doesn't yet exist. Per-file release metadata for batch jobs, further Activity/job-detail UI work, and provenance sidecar fields are also out of scope for this stage.
- This is Stage 3 of 3, completing the post-0.6.0 combined TODO's primary goals.

## [0.8.0] - 2026-09-04

### Added

- Wired Stage 1's resolved release metadata into translation (Stage 2 of the post-0.6.0 combined TODO): a job's resolved `release_title`/`release_story` are now included in the translation system prompt as a dedicated, clearly-labeled background-context block, after the glossary and before per-file translation memory. The block always carries an explicit guardrail: the release title/story are background only, never dialogue to copy or invent, used only to resolve proper names, terminology, roles, relationships, and scenario ambiguity - and the spoken Japanese is always authoritative if it conflicts with the release context.
- Extended `title_or_series_overrides` matching (translation glossary only) to also try the resolved `release_title` as a secondary lookup key when the release's own catalog/DVD code (`release_external_id`) doesn't match an override entry directly. The catalog/DVD code is always tried first and wins on a match, per the existing "most specific match wins" precedence; recognition vocabulary (ASR-side) matching is unchanged and still uses only the catalog/DVD code, since that stays out of scope for this stage.
- Added `translation_release_title_context_used`/`translation_release_story_context_used` diagnostics to the job result, reporting whether release-context background was actually included in the translation prompt for a given file.
- The job pipeline now resolves the translation glossary/vocabulary lookup key from `release_external_id` (falling back to `external_id` per Stage 1's existing guarantee) instead of `external_id` directly - identical behavior for every existing job, and the more correct catalog-identity key going forward.

### Notes

- Translation-only: recognition (Qwen/Whisper/ForcedAligner/VAD), the subtitle normalizer, provenance hashing, and the prompt-leak vocalization filter are unchanged.
- Free-text extraction of proper names/terminology from title/story prose, and proper-name variant detection/flagging, are explicitly out of scope for this stage (deferred to Stage 3) to avoid fabrication risk from ad hoc NLP heuristics on mixed Japanese/English marketing prose.
- This is Stage 2 of 3: mixed-script translation QA, canonical text-density QA, and proper-name variant detection (Stage 3) are not part of this release.

## [0.7.0] - 2026-09-04

### Added

- Added optional release metadata support (Stage 1 of the post-0.6.0 combined TODO): jobs may now carry `release_external_id` (a catalog/DVD code such as `ADN-803`), `javbeacon_release_id` (JAVBeacon's internal database id), and manual `release_title`/`release_story`, all optional and fully backwards compatible with existing clients and persisted jobs. `release_external_id` defaults from the existing `external_id` field when omitted.
- Added a `internal/release` client and deterministic lookup resolver against a real JAVBeacon instance (`GET /api/releases/{id}` and `GET /api/releases?video_id=...`, Bearer-token authenticated), with a strict, never-reversed precedence: `javbeacon_release_id` > `release_external_id` > filename fallback > none. An internal ID that doesn't resolve, or an internal/external ID mismatch, or an external ID matching more than one release, all fail job creation with a clear diagnostic rather than silently guessing; a JAVBeacon instance that is simply unreachable is non-fatal and falls back to manual metadata (surfaced via `release_lookup_error`). New optional `javbeacon.base_url`/`javbeacon.api_key`/`javbeacon.timeout_seconds` config section (also `JAVBEACONSUBS_JAVBEACON_BASE_URL`/`JAVBEACONSUBS_JAVBEACON_API_KEY` env vars); lookups are simply unavailable when unconfigured.
- Added `release_title_source`/`release_story_source` (`manual` | `javbeacon` | `legacy`), `release_lookup_method`/`release_lookup_matched`/`release_lookup_error`, and `release_provider` diagnostics to the job record and API response, so it's always clear where release metadata came from and whether a lookup was attempted.
- Added a "Release Context" field group (catalog ID, JAVBeacon release ID, title, story) to the job creation UI, and a Release summary line on each job card. When a matched release is also locally present in the operator's StashApp library (via JAVBeacon's own Stash sync), the job card shows the Stash scene ID with a direct "Open in StashApp" link.

### Notes

- Manual `release_title`/`release_story` always take precedence over a JAVBeacon match for those two fields; a JAVBeacon match still populates `release_lookup_matched`/`release_provider` either way.
- This is Stage 1 of 3: release metadata is not yet wired into translation context or `title_or_series_overrides` matching (Stage 2), and mixed-script translation QA / canonical text-density QA (Stage 3) are not part of this release. Recognition, timing, translation, and the prompt-leak vocalization filter are unchanged.

## [0.6.0] - 2026-09-03

### Fixed

- Fixed the prompt-leak tiny-vocalization guard (added in 0.5.0) under-suppressing phantom "Yes."/「はい」 recoveries in the 0.35-0.47 speech-probability band. Replaced the single `speech_probability < 0.28` threshold with a deterministic multi-signal suspicion score: weak/moderate speech probability, an ambiguous VAD classification, a tiny transcript on a long region, `identical_neighbors` on the original, and — the dominant signal — the same generic vocalization token recurring across nearby prompt-leak retries within a local segment/time window. Strong direct evidence (clean "speech" classification with high speech probability) overrides the score and still preserves the recovery. Direct Qwen vocalization recognition without prompt leakage, and substantive prompt-leak retry text, are unaffected; suppressed cases still never escalate to Whisper in Balanced. Added `prompt_leakage_retry_vocalization_repetition_rejected`, `..._weak_timing_rejected`, and `..._strong_evidence_preserved` diagnostics.

## [0.5.0] - 2026-09-02

### Fixed

- Fixed a subtitle-splitting artifact where a cue boundary could land in the middle of an ellipsis, leaving a leading ".." fragment orphaned at the start of the next child cue (e.g. "...so." / "..  Ah, ..."). Ellipses (ASCII "...", ".." and Unicode "…") now always stay attached to the phrase that precedes them; any leftover fragment is relocated rather than dropped, so no text is lost. Covered by a dedicated ellipsis-boundary test suite and a strengthened ADN-803 regression.
- Fixed two-line subtitle wrapping to balance the two output lines by visible character length instead of raw midpoint proximity, avoiding a very short line next to a much longer one.
- Fixed prompt-leak retries that recover only a tiny generic vocalization (e.g. はい/うん/あ/え/ん) with weak or ambiguous speech evidence from being automatically accepted as recovered dialogue. These are now classified as an ambiguous vocalization and preferred silent instead of surfacing either the leaked prompt text or an unsupported guess, while genuine short utterances and vocalizations backed by strong timing/audio confidence are still preserved and never escalate to Whisper.

### Added

- Added diagnostic-only subtitle normalizer summary logging (over-target/over-hard-target line counts, max line length, split-method counts, punctuation-repair count) and a `prompt_leakage_retry_ambiguous_vocalization` recognition pipeline metric.

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
