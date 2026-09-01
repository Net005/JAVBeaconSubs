# JAVBeacon Subtitles

A Go subtitle service optimized for Japanese dialogue in JAV, GIGA/tokusatsu, Akiba-web, and similarly difficult material. It accepts a browser upload, exact media files, or folders; queues jobs; extracts clean mono audio with FFmpeg; runs a Japanese-forced Qwen-first recognition pipeline; and writes atomic SRT, ASS, and diagnostic JSON outputs. Jobs survive restarts in an embedded SQLite database.

## Why this pipeline is different

The old implementation auto-detected the language, removed short lines, and sent isolated fragments to MyMemory/Google-free translation. The primary pipeline now forces Japanese in Qwen3-ASR-1.7B, uses recall-biased padded dialogue detection, validates suspicious text before timing it, and rejects impossible or out-of-order timestamps instead of silently collapsing hours of audio into one subtitle.

Balanced mode (the default) runs [Qwen3-ASR-1.7B](https://huggingface.co/Qwen/Qwen3-ASR-1.7B) for every detected region, ReazonSpeech NeMo v2 only when a result is suspicious, and Whisper Large-v3 only when the two Japanese systems materially disagree. [Qwen3-ForcedAligner-0.6B](https://huggingface.co/Qwen/Qwen3-ForcedAligner-0.6B) is loaded after the accepted Japanese text has been selected, so timing can never be mistaken for transcription validation. Fast mode skips multi-ASR verification; High Accuracy verifies every detected region with Reazon. Models run in separate lifecycle phases so they do not all occupy GPU memory at once.

Two English modes are available:

- `direct`: the Qwen Japanese transcript remains canonical, while the existing Whisper speech-to-English path supplies a legacy fully local English track. It is retained for compatibility, not maximum quality.
- `contextual`: the validated Japanese transcript is translated through an OpenAI-compatible chat endpoint with neighboring dialogue and an optional glossary. This is the recommended high-quality mode. Ollama's `/v1`, LocalAI, vLLM, and compatible hosted APIs work.
- `none`: Japanese transcription only.

Go remains the service and orchestration layer. The Qwen/Reazon pipeline runs in a dedicated Python worker process per media file; `whisper.cpp` remains a tertiary fallback and powers legacy direct translation.

## Docker Compose for NVIDIA GPUs (CUDA)

The default Compose stack is NVIDIA/CUDA accelerated and uses host GPU device `0` by default. It includes pinned ReazonSpeech v3 tooling and a pinned whisper.cpp fallback with CUDA VMM disabled, explicitly reserves the NVIDIA device, and enables the `compute` capability for CUDA plus `utility` for `nvidia-smi` health checks. Install the NVIDIA driver and NVIDIA Container Toolkit on the Docker host first.

The CUDA image defaults to architecture targets `75;86` and two compiler workers. `CUDA_ARCHITECTURES` can be changed for a different NVIDIA generation, and `WHISPER_BUILD_JOBS` can be raised on build hosts with more memory. The conservative defaults prevent GitHub-hosted runners from being overwhelmed by simultaneous CUDA compiler processes.

Compose bind-mounts the configurable `JAVBEACONSUBS_MODELS_PATH` at `/models`. Qwen3-ASR-1.7B, Qwen3-ForcedAligner-0.6B, ReazonSpeech NeMo v2, the checksum-verified Whisper fallback, and Silero assets are downloaded on first startup into that persistent cache. Hugging Face snapshots and ready markers reuse unchanged model revisions across container rebuilds; model weights are never baked into the image. `JAVBEACONSUBS_DATA_PATH` is mounted at `/data` for SQLite and uploaded files.

Qwen ASR/aligner and ReazonSpeech are Apache-2.0 licensed. Qwen integration uses the official `qwen-asr` 0.0.6 package, while ReazonSpeech tooling is pinned to its official v3.0.0 commit. The first image build is substantially larger than whisper.cpp-only because it includes PyTorch and NVIDIA NeMo; subsequent GHCR builds reuse BuildKit layers, while model weights remain in the host-mounted cache rather than the image.

Set `PUID` and `PGID` in `.env` to the numeric IDs of the host account that should own generated subtitles, uploads, and SQLite data (`id -u` and `id -g` show the usual values). `GID` and `GUID` are accepted as compatibility aliases when `PGID` is blank. The container prepares models as root, fixes ownership of `/data`, and then runs JAVBeaconSubs as that unprivileged numeric user/group. Every media bind mount must grant that owner write access; generated SRT files are group-readable and group-writable.

```sh
cp .env.example .env
# Set an external API token and any host paths you want to expose.
docker compose up --build -d
docker compose logs -f javbeaconsubs
```

Verify that Docker can see the NVIDIA GPU before starting the service:

```sh
docker run --rm --gpus all nvidia/cuda:12.9.0-base-ubuntu22.04 nvidia-smi
```

Open `http://localhost:8097`, or the port selected by `SUBTITLE_PORT`. The application listener, published port, and health check all use that same value. Application state, uploaded media, and `javbeaconsubs.db` live under `JAVBEACONSUBS_DATA_PATH`. Models live under `JAVBEACONSUBS_MODELS_PATH`, outside the Docker build context, so rebuilding or replacing the container does not copy or download `large-v3` again.

When upgrading an installation that used the previous named Docker volume, copy its database and uploads into `JAVBEACONSUBS_DATA_PATH` before starting this version. Docker does not migrate named-volume contents into a host bind mount automatically; the old volume is not deleted.

`NVIDIA_GPU_DEVICE_ID=0` selects the first GPU. Run `nvidia-smi -L` on the host and change it in `.env` when the intended NVIDIA GPU has another device index. The model revision is pinned to upstream SHA-1 `ad82bf6a9043ceed055076d0fd39f5f186ff8062`. A verified marker avoids hashing 2.9 GiB on every start; the model is replaced only when missing, corrupt, or the pinned revision changes.

For a host without NVIDIA support, use the standalone CPU definition:

```sh
docker compose -f compose.cpu.yaml up --build -d
```

`MEDIA_PATH` is only a convenient default mount at `/media`; it is not an allowlist. The service accepts any file or folder path visible inside the container and never scans a mount automatically. Add as many read/write bind mounts as needed, preferably preserving the same host and container path so JAVBeacon can submit paths without translation:

```yaml
services:
  javbeaconsubs:
    environment:
      PUID: "1000"
      PGID: "1000"
    volumes:
      - "/mnt/data/movies:/mnt/data/movies"
      - "/srv/archive:/srv/archive"
```

To attach this service to an existing JAVBeacon Compose network, set `JAVBEACON_NETWORK` to that network's actual Docker name and add the optional overlay:

```sh
docker compose -f compose.yaml -f compose.javbeacon.yaml up --build -d
```

JAVBeacon can then address this API by its Compose service name, `http://javbeaconsubs:8097`. Keep `MEDIA_PATH` pointed at the same host media tree JAVBeacon uses so a submitted JAVBeacon path maps predictably to `/media/...` here.

## Native install

Requirements: Go 1.25+, FFmpeg, Python 3, `qwen-asr` 0.0.6, Qwen3-ASR-1.7B, Qwen3-ForcedAligner-0.6B, ReazonSpeech NeMo ASR v3 tooling, and ReazonSpeech NeMo v2. `whisper-cli` plus multilingual `large-v3` are required when tertiary fallback or legacy direct translation is enabled.

```sh
cp config.example.json config.json
# Edit the model paths.
go test ./...
go build -o javbeaconsubs ./cmd/javbeaconsubs
./javbeaconsubs -config config.json
```

Open `http://127.0.0.1:8097`.

There is no application-level path allowlist. Native installs can process any readable path supplied through the API. Containers can process any path mounted into the container. Because submitted paths can cause media to be read and subtitles to be written beside it, protect remotely reachable installations with `JAVBEACONSUBS_API_TOKEN` and a trusted reverse proxy.

Authentication and translation credentials have separate purposes:

- The web interface uses a username/password account stored in SQLite. On first launch it asks you to create the administrator account, then uses a secure, HTTP-only session cookie. Optional `JAVBEACONSUBS_WEB_USERNAME` and `JAVBEACONSUBS_WEB_PASSWORD` values can seed the first account for unattended deployments.
- `JAVBEACONSUBS_API_TOKEN` is only for JAVBeacon, curl, and other external REST clients. Send it as `Authorization: Bearer <token>` or `X-API-Key: <token>`. It is never required in the browser after web login.
- `JAVBEACONSUBS_TRANSLATION_API_KEY` is sent as a Bearer credential to the configured OpenAI-compatible endpoint only when contextual translation is used. It is not used to access JAVBeaconSubs. Leave it blank for direct Whisper translation or an unauthenticated local endpoint such as Ollama.

## Web interface

The job form has two explicit modes:

- **Upload one file** streams one video/audio file into managed storage. When processing finishes, English SRT/ASS, optional Japanese SRT/ASS, and diagnostic JSON downloads appear on its job card.
- **Server paths** accepts one or more exact files and/or folders already visible to the service. Folder traversal happens only when requested.

Each job selects Fast, Balanced, or High Accuracy recognition plus a small `jav`, `tokusatsu`, `akiba`, or `standard` context profile. The profile biases names and domain terms without injecting one enormous vocabulary into every weak audio region. **Also keep Japanese** writes `.ja.srt` and `.ja.ass` from the already-canonical Japanese transcript without another ASR pass. Japanese-only mode always writes them because they are the primary output.

The separate **Settings** tab switches between direct local translation, Japanese-only transcription, and higher-quality contextual translation. It stores the endpoint URL, model, key, batching, timeout, scene-gap threshold, translation-memory preference, and glossaries in SQLite. Saved API keys are never returned to the browser; leaving the key blank keeps the existing value.

Contextual requests use scene-aware windows with up to three preceding rows and one following row, stopping at the configured `context_gap_ms` silence boundary (8 seconds by default). Timestamps remain local and are never included in translation requests. Rows are serialized compactly as `[id,t,text]`, and a payload budget prevents the selected window from exceeding the previous serializer's request payload for the same transcript and batch.

The legacy free-form `glossary` remains supported. The optional `structured_glossary` separates global `style` rules from Japanese `terms`; style rules are always included, while a term mapping is sent only when its Japanese source appears in that request's dialogue. There is no fixed mapping-count limit, and large glossaries are indexed once per translation job so each batch checks only plausible candidates. Optional exact translation memory is scoped to one submitted job, normalizes whitespace, and deliberately excludes very short reactions.

The Settings tab can import UTF-8 `.txt` mapping files using one `Japanese=English` entry per line. Blank lines and identical duplicate sources are removed automatically. Conflicting translations for the same normalized Japanese source are rejected instead of silently choosing one. Imports merge with mappings already in the editor and remain local to the browser until **Save settings** is selected.

The repository includes an optional [Japanese-to-English JAV base glossary](mappings/japanese-english-jav-mappings-base.txt) that can be imported from the Settings tab and adapted for your collection. It focuses on high-confidence vocabulary used in JAV, GIGA/tokusatsu heroine action, and Akiba-web-style adult/action content. It is intentionally not a general Japanese dictionary: ambiguous everyday words are excluded where a forced mapping could damage the contextual translation. Only mappings whose Japanese term occurs in the current dialogue window are sent to the contextual translation provider.

The same tab can run post-processing after a successful subtitle job. Choose a Bash script mounted under `/scripts`, or an HTTP webhook sent through curl. The full terminal job JSON is supplied to standard input/request body. Bash scripts also receive `JAVBEACONSUBS_JOB_ID`, `JAVBEACONSUBS_EXTERNAL_ID`, `JAVBEACONSUBS_STATUS`, and `JAVBEACONSUBS_FILES`. Both Bash and curl are included in the image, and post-processing status/errors appear on the job card.

`JAVBEACONSUBS_SCRIPTS_PATH` controls the host directory mounted read-only at `/scripts`. Post-processing settings and write-only webhook credentials persist in SQLite.

Running activity cards show the current absolute path, filename, file number, recognition mode/profile, live ASR phase progress, and an estimated completion time. The Qwen worker reports dialogue detection, primary transcription, conditional fallback, alignment, and completion rather than appearing stalled during a monolithic inference call.

SQLite stores the job request, state, progress, results, and JAVBeacon correlation ID. Queued or running jobs found after an unclean restart are marked failed rather than silently left running forever. SQLite uses WAL mode and a busy timeout; one database file is sufficient for the deliberately small worker pool.

## Translation usage and limits

The default `translation.mode=direct` uses local whisper.cpp and therefore consumes no OpenAI API tokens, ChatGPT messages, or Codex five-hour/weekly allowance.

The example contextual endpoint, `http://host.docker.internal:11434/v1`, is local Ollama. It also consumes no OpenAI usage. “OpenAI-compatible” describes the HTTP protocol; it does not mean requests automatically go to OpenAI.

If `translation.base_url` is changed to `https://api.openai.com/v1` and an OpenAI API key is supplied, calls are billed to that API account using the selected model's input/output token rates. API billing and API rate limits are separate from ChatGPT/Codex plan limits. Exact usage depends on dialogue length; completed job results record `translation_input_tokens`, `translation_output_tokens`, and `translation_total_tokens` whenever the endpoint returns standard usage metadata. Logs also report translated, context, reused, and included-glossary row counts without dialogue text or credentials, making an old/new movie A/B comparison straightforward.

## CUDA recovery and VRAM behavior

Each media file runs in a separate ASR worker process. When it exits—even after cancellation—the OS and NVIDIA driver destroy that process's CUDA context. Within a normal job, Qwen ASR is explicitly released before Reazon fallback or the forced aligner is loaded; CUDA caches and IPC allocations are cleared between phases. Models are reused across the relevant segment batch but are not kept resident between jobs. The service also probes `nvidia-smi` before inference and logs memory state afterward.

Automatic GPU reset is disabled by default because NVIDIA refuses to reset a primary/display GPU. If the Qwen worker fails, its process exit first releases its CUDA context; configured CPU and whole-file compatibility fallbacks can then run. Whisper output is rejected when a segment exceeds the configured safety limit, preventing the observed multi-hour tail from being reported as successful.

On a dedicated compute GPU, guarded reset can be enabled with `JAVBEACONSUBS_GPU_AUTO_RESET=true`. The service will try the reset and one GPU retry, then still fall back to CPU when enabled. A genuinely wedged host driver can require a host driver/module reload or reboot; a container cannot safely force-reset the active display GPU.

CPU fallback can be disabled when a hard failure is preferred:

```sh
JAVBEACONSUBS_GPU_FALLBACK_CPU=false docker compose up -d
```

## JAVBeacon REST contract

Create one job from any mixture of files and folders. This curl example recursively scans `/mnt/data/movies` while preserving existing subtitle files:

```sh
curl --fail-with-body --request POST 'http://localhost:8097/api/v1/jobs' \
  --header 'Authorization: Bearer YOUR_API_TOKEN' \
  --header 'Content-Type: application/json' \
  --data '{
    "inputs": ["/mnt/data/movies"],
    "recursive": true,
    "overwrite": false,
    "keep_japanese": true,
    "asr_mode": "balanced",
    "asr_profile": "jav",
    "debug_mode": false
  }'
```

The equivalent request body, including optional JAVBeacon integration fields, is:

```http
POST /api/v1/jobs
Content-Type: application/json

{
  "inputs": [
    "/mnt/data/movies/ABC-123.mkv",
    "/srv/archive/incoming/batch-7"
  ],
  "recursive": true,
  "overwrite": false,
  "keep_japanese": true,
  "asr_mode": "high_accuracy",
  "asr_profile": "tokusatsu",
  "debug_mode": true,
  "external_id": "javbeacon-movie-4182",
  "callback_url": "http://127.0.0.1:8080/api/subtitles/callback"
}
```

The response is `202 Accepted`, includes the job, and sets `Location: /api/v1/jobs/{id}`. JAVBeacon can then:

- `GET /api/v1/jobs/{id}` — poll one job.
- `GET /api/v1/jobs` — recent jobs.
- `GET /api/v1/events` — server-sent `job` events for live UI updates.
- `DELETE /api/v1/jobs/{id}` — cancel queued/running work.
- `GET /api/v1/health` — dependency and model readiness.
- `GET /api/v1/settings` and `PUT /api/v1/settings` — read/update persistent translation and post-processing settings (credentials are write-only).

The browser single-file endpoint is multipart `POST /api/v1/jobs/upload` with a `file` field and optional `external_id`, `callback_url`, `overwrite`, `keep_japanese`, `asr_mode`, `asr_profile`, and `debug_mode` fields. For JSON and multipart jobs, omitting `keep_japanese` uses `output.keep_japanese` from service configuration. Generated files are available through `GET /api/v1/jobs/{id}/outputs/{zeroBasedResultIndex}/{en|ja|en-ass|ja-ass|json}`.

If `callback_url` is supplied, the terminal job document is POSTed there. `external_id` round-trips unchanged so JAVBeacon can associate it with its own movie or task record.

External clients send `Authorization: Bearer …` or `X-API-Key: …` to `/api/*` requests. Browser requests are authorized by the web login session instead; the browser never stores or asks for the external API token.

## Backups and updates

Back up `JAVBEACONSUBS_DATA_PATH` to preserve SQLite history and uploaded files. SQLite's main database, WAL, and SHM files must be captured together while the service is running; the simplest consistent backup is to stop the container first. `docker compose down` preserves both host directories. Models are ordinary files under `JAVBEACONSUBS_MODELS_PATH`.

## Quality profile

For the highest-quality Japanese-to-English output, use the default Qwen backend, High Accuracy for the hardest titles (Balanced is usually the better speed/quality tradeoff), GPU acceleration, and `translation.mode=contextual` with a strong Japanese-capable instruction model. Put global preferences in `structured_glossary.style` and recurring Japanese names or vocabulary in `structured_glossary.terms`; the legacy `translation.glossary` remains available. `direct` mode avoids a separate translation model but remains a compatibility path without wider dialogue context.

The Qwen path uses recall-biased, configurable pre/post padding and a 30-second maximum region. Ambiguous vocalizations are retained and scored rather than expanded into invented sentences. Do not compensate for noisy recognition by deleting all short segments—Japanese reactions are often short.
