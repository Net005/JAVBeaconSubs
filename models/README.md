# Model cache

Docker Compose mounts this project directory at `/models`. On first startup the entrypoint downloads the checksum-verified `ggml-large-v3.bin` Balanced fallback and the Silero VAD model here. ReazonSpeech NeMo v2 is cached only when its standalone backend or explicit compatibility flag is enabled. Subsequent builds and container replacements reuse these files; Reazon uses the `reazonspeech/` Hugging Face cache subdirectory and a model-name marker.

The 2.9 GiB `ggml-large-v3.bin` is intentionally ignored by Git: committing it would make every clone and repository operation much heavier. Its expected upstream SHA-1 is pinned in `docker/entrypoint.sh`; a new download happens only when the file is absent, invalid, or that pinned revision changes.
