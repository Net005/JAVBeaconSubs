# Model cache

Docker Compose mounts this project directory at `/models`. On first startup the entrypoint downloads ReazonSpeech NeMo v2, the checksum-verified `ggml-large-v3.bin` fallback, and the Silero VAD model here. Subsequent builds and container replacements reuse these files. ReazonSpeech uses the `reazonspeech/` Hugging Face cache subdirectory and a model-name marker to avoid loading or downloading the weights during later startup checks.

The 2.9 GiB `ggml-large-v3.bin` is intentionally ignored by Git: committing it would make every clone and repository operation much heavier. Its expected upstream SHA-1 is pinned in `docker/entrypoint.sh`; a new download happens only when the file is absent, invalid, or that pinned revision changes.
