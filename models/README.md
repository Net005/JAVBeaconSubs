# Model cache

Docker Compose mounts this project directory at `/models`. On first startup the entrypoint downloads and verifies `ggml-large-v3.bin` and the Silero VAD model here. Subsequent builds and container replacements reuse these files.

The 2.9 GiB `ggml-large-v3.bin` is intentionally ignored by Git: committing it would make every clone and repository operation much heavier. Its expected upstream SHA-1 is pinned in `docker/entrypoint.sh`; a new download happens only when the file is absent, invalid, or that pinned revision changes.
