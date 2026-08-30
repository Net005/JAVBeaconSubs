ARG WHISPER_IMAGE=ghcr.io/ggml-org/whisper.cpp:main-cuda
FROM golang:1.25-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/javbeaconsubs ./cmd/javbeaconsubs

FROM ${WHISPER_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl ffmpeg && rm -rf /var/lib/apt/lists/*
COPY --from=go-builder /out/javbeaconsubs /usr/local/bin/javbeaconsubs
COPY config.docker.json /app/config.json
COPY docker/entrypoint.sh /usr/local/bin/javbeaconsubs-entrypoint
RUN chmod 0755 /usr/local/bin/javbeaconsubs-entrypoint && mkdir -p /data/uploads /models
ENV JAVBEACONSUBS_LISTEN=0.0.0.0:8097 \
    JAVBEACONSUBS_DATABASE_PATH=/data/javbeaconsubs.db \
    JAVBEACONSUBS_UPLOAD_DIR=/data/uploads \
    JAVBEACONSUBS_WHISPER_BINARY=/app/build/bin/whisper-cli \
    JAVBEACONSUBS_WHISPER_MODEL=/models/ggml-large-v3.bin \
    JAVBEACONSUBS_VAD_MODEL=/models/ggml-silero-v6.2.0.bin \
    JAVBEACONSUBS_DOWNLOAD_MODELS=true
EXPOSE 8097
VOLUME ["/data", "/models"]
ENTRYPOINT ["/usr/local/bin/javbeaconsubs-entrypoint"]
CMD ["/usr/local/bin/javbeaconsubs", "-config", "/app/config.json"]
