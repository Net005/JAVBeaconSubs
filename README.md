# JAVBeacon Subtitles

A Go subtitle service optimized for Japanese dialogue. It accepts a browser upload, exact media files, or folders; queues jobs; extracts clean mono audio with FFmpeg; transcribes with `whisper.cpp`; removes overlapping duplicate detections; and writes atomic SRT files. Jobs survive restarts in an embedded SQLite database.

## Why this pipeline is different

The old implementation auto-detected the language, disabled VAD, removed short lines, and sent isolated fragments to MyMemory/Google-free translation. This service fixes Japanese (`ja`) explicitly, keeps short reactions, uses padded Silero VAD, normalizes the audio, constrains line length, deduplicates only overlapping detections, and supports dialogue-window translation.

Two English modes are available:

- `direct`: Whisper translates Japanese speech directly to English. It is completely local and is the easiest reliable starting point.
- `contextual`: Whisper first produces Japanese, then an OpenAI-compatible chat endpoint translates batches with neighboring dialogue and an optional glossary. This normally produces the most natural subtitles. Ollama's `/v1`, LocalAI, vLLM, and compatible hosted APIs work.
- `none`: Japanese transcription only.

Go is the service and orchestration layer. `whisper.cpp` and FFmpeg are native executables invoked without a Python runtime.

## Docker Compose for NVIDIA GPUs (CUDA)

The default Compose stack is NVIDIA/CUDA accelerated and uses host GPU device `0` by default. It builds a pinned whisper.cpp release with CUDA VMM disabled for better stability after gaming and sleep/resume, explicitly reserves the NVIDIA device, and enables the `compute` capability for CUDA plus `utility` for `nvidia-smi` health checks. Install the NVIDIA driver and NVIDIA Container Toolkit on the Docker host first.

This project already contains the verified `models/ggml-large-v3.bin`; Compose bind-mounts the configurable `JAVBEACONSUBS_MODELS_PATH` at `/models`. The smaller Silero VAD model is downloaded on first startup. `JAVBEACONSUBS_DATA_PATH` is mounted at `/data` for SQLite and uploaded files. Both default to project-local directories, so container replacement does not discard them.

```sh
cp .env.example .env
# Set MEDIA_PATH in .env to the host folder JAVBeacon and this service share.
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

`MEDIA_PATH` is mounted at `/media` read/write because subtitles are written beside server-side media. The service never scans it automatically: JAVBeacon or the web UI must submit a specific file or folder. The application accepts any readable file or folder path; Docker mounts remain the real visibility boundary. Add extra read/write bind mounts under `services.javbeaconsubs.volumes` when libraries live outside `MEDIA_PATH`.

To attach this service to an existing JAVBeacon Compose network, set `JAVBEACON_NETWORK` to that network's actual Docker name and add the optional overlay:

```sh
docker compose -f compose.yaml -f compose.javbeacon.yaml up --build -d
```

JAVBeacon can then address this API by its Compose service name, `http://javbeaconsubs:8097`. Keep `MEDIA_PATH` pointed at the same host media tree JAVBeacon uses so a submitted JAVBeacon path maps predictably to `/media/...` here.

## Native install

Requirements: Go 1.25+, FFmpeg, `whisper-cli`, a multilingual `large-v3` GGML model, and the whisper.cpp Silero VAD model.

```sh
cp config.example.json config.json
# Edit the model paths.
go test ./...
go build -o javbeaconsubs ./cmd/javbeaconsubs
./javbeaconsubs -config config.json
```

Open `http://127.0.0.1:8097`.

There is no application-level path allowlist. Native installs can process any readable path supplied through the API. Containers can process any path mounted into the container. Because submitted paths can cause media to be read and subtitles to be written beside it, protect remotely reachable installations with `JAVBEACONSUBS_API_TOKEN` and a trusted reverse proxy.

The two `.env` credentials have separate purposes:

- `JAVBEACONSUBS_API_TOKEN` protects this service's web and REST API. JAVBeacon and other clients send the same value as `Authorization: Bearer <token>` or `X-API-Key: <token>`. Leaving it blank disables API authentication.
- `JAVBEACONSUBS_TRANSLATION_API_KEY` is sent as a Bearer credential to the configured OpenAI-compatible endpoint only when contextual translation is used. It is not used to access JAVBeaconSubs. Leave it blank for direct Whisper translation or an unauthenticated local endpoint such as Ollama.

## Web interface

The job form has two explicit modes:

- **Upload one file** streams one video/audio file into managed storage. When processing finishes, English and optional Japanese SRT downloads appear on its job card.
- **Server paths** accepts one or more exact files and/or folders already visible to the service. Folder traversal happens only when requested.

The permanent **Translation settings** panel switches between direct local translation, Japanese-only transcription, and higher-quality contextual translation. It stores the endpoint URL, model, key, batching, timeout, and glossary in SQLite. Saved API keys are never returned to the browser; leaving the key blank keeps the existing value.

Running activity cards show the current absolute path, filename, file number, live Whisper progress, and an estimated completion time. The ETA becomes more accurate after the first few percent of work.

SQLite stores the job request, state, progress, results, and JAVBeacon correlation ID. Queued or running jobs found after an unclean restart are marked failed rather than silently left running forever. SQLite uses WAL mode and a busy timeout; one database file is sufficient for the deliberately small worker pool.

## Translation usage and limits

The default `translation.mode=direct` uses local whisper.cpp and therefore consumes no OpenAI API tokens, ChatGPT messages, or Codex five-hour/weekly allowance.

The example contextual endpoint, `http://host.docker.internal:11434/v1`, is local Ollama. It also consumes no OpenAI usage. “OpenAI-compatible” describes the HTTP protocol; it does not mean requests automatically go to OpenAI.

If `translation.base_url` is changed to `https://api.openai.com/v1` and an OpenAI API key is supplied, calls are billed to that API account using the selected model's input/output token rates. API billing and API rate limits are separate from ChatGPT/Codex plan limits. Exact usage depends on dialogue length; completed job results now record `translation_input_tokens`, `translation_output_tokens`, and `translation_total_tokens` whenever the endpoint returns standard usage metadata.

## CUDA recovery and VRAM behavior

Each media file runs in a separate `whisper-cli` process. When it exits—even after cancellation—the OS and NVIDIA driver destroy that process's CUDA context, releasing all VRAM owned by the subtitle worker. The service also probes `nvidia-smi` before every GPU inference and logs memory state after the worker exits.

Automatic GPU reset is disabled by default because NVIDIA refuses to reset a primary/display GPU. If CUDA preflight or inference fails, the current file is retried on CPU instead of failing the job. The image also compiles whisper.cpp with `GGML_CUDA_NO_VMM=ON`, avoiding the CUDA virtual-memory path implicated by startup crashes that stop immediately after `ggml_cuda_init` reports `VMM: yes`.

On a dedicated compute GPU, guarded reset can be enabled with `JAVBEACONSUBS_GPU_AUTO_RESET=true`. The service will try the reset and one GPU retry, then still fall back to CPU when enabled. A genuinely wedged host driver can require a host driver/module reload or reboot; a container cannot safely force-reset the active display GPU.

CPU fallback can be disabled when a hard failure is preferred:

```sh
JAVBEACONSUBS_GPU_FALLBACK_CPU=false docker compose up -d
```

## JAVBeacon REST contract

Create one job from any mixture of files and folders:

```http
POST /api/v1/jobs
Content-Type: application/json

{
  "inputs": [
    "/media/library/ABC-123.mkv",
    "/media/incoming/batch-7"
  ],
  "recursive": true,
  "overwrite": false,
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
- `GET /api/v1/settings` and `PUT /api/v1/settings` — read/update persistent translation settings (API keys are write-only).

The browser single-file endpoint is multipart `POST /api/v1/jobs/upload` with a `file` field and optional `external_id`, `callback_url`, and `overwrite` fields. Generated files can be retrieved through `GET /api/v1/jobs/{id}/outputs/{zeroBasedResultIndex}/{en|ja}`.

If `callback_url` is supplied, the terminal job document is POSTed there. `external_id` round-trips unchanged so JAVBeacon can associate it with its own movie or task record.

When `api_token` is configured, send `Authorization: Bearer …` or `X-API-Key: …` to `/api/*` requests. The web interface has a token field stored locally in that browser.

## Backups and updates

Back up `JAVBEACONSUBS_DATA_PATH` to preserve SQLite history and uploaded files. SQLite's main database, WAL, and SHM files must be captured together while the service is running; the simplest consistent backup is to stop the container first. `docker compose down` preserves both host directories. Models are ordinary files under `JAVBEACONSUBS_MODELS_PATH`.

## Quality profile

For the highest-quality Japanese-to-English output, use `ggml-large-v3.bin`, GPU acceleration, and `translation.mode=contextual` with a strong Japanese-capable instruction model. Put recurring names, honorific preferences, and domain vocabulary in `translation.glossary`. `direct` mode is faster and avoids a second model, but it cannot use wider dialogue context or produce the Japanese sidecar.

The default VAD profile intentionally uses 100 ms minimum speech plus 320 ms padding to retain short replies that older region detectors commonly miss. If very quiet speech is still absent, lower `vad_threshold` toward `0.35`; if noise creates false lines, raise it toward `0.50`. Do not compensate by deleting all short segments—Japanese reactions often are short.
