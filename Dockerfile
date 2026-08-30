ARG WHISPER_IMAGE=ghcr.io/ggml-org/whisper.cpp:main-cuda
FROM golang:1.25-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/beaconsubs ./cmd/beaconsubs

FROM ${WHISPER_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl ffmpeg && rm -rf /var/lib/apt/lists/*
COPY --from=go-builder /out/beaconsubs /usr/local/bin/beaconsubs
COPY config.docker.json /app/config.json
COPY docker/entrypoint.sh /usr/local/bin/beaconsubs-entrypoint
RUN chmod 0755 /usr/local/bin/beaconsubs-entrypoint && mkdir -p /data/uploads /models
ENV BEACONSUBS_LISTEN=0.0.0.0:8097 \
    BEACONSUBS_DATABASE_PATH=/data/beaconsubs.db \
    BEACONSUBS_UPLOAD_DIR=/data/uploads \
    BEACONSUBS_WHISPER_BINARY=/app/build/bin/whisper-cli \
    BEACONSUBS_WHISPER_MODEL=/models/ggml-large-v3.bin \
    BEACONSUBS_VAD_MODEL=/models/ggml-silero-v6.2.0.bin \
    BEACONSUBS_DOWNLOAD_MODELS=true
EXPOSE 8097
VOLUME ["/data", "/models"]
ENTRYPOINT ["/usr/local/bin/beaconsubs-entrypoint"]
CMD ["/usr/local/bin/beaconsubs", "-config", "/app/config.json"]
