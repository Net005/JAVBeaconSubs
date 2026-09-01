ARG CUDA_VERSION=13.0.0
ARG UBUNTU_VERSION=22.04
FROM golang:1.25-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/javbeaconsubs ./cmd/javbeaconsubs

FROM nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION} AS whisper-builder
ARG WHISPER_CPP_REF=f049fff95a089aa9969deb009cdd4892b3e74916
ARG CUDA_ARCHITECTURES=75;86
ARG WHISPER_BUILD_JOBS=2
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates cmake git && rm -rf /var/lib/apt/lists/*
RUN git init && git remote add origin https://github.com/ggml-org/whisper.cpp.git && git fetch --depth 1 origin ${WHISPER_CPP_REF} && git checkout --detach FETCH_HEAD
RUN cmake -S . -B build -DGGML_CUDA=ON -DGGML_CUDA_NO_VMM=ON -DCMAKE_CUDA_ARCHITECTURES="${CUDA_ARCHITECTURES}" -DCMAKE_BUILD_TYPE=Release && cmake --build build --config Release --target whisper-cli --parallel "${WHISPER_BUILD_JOBS}"

FROM nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu${UBUNTU_VERSION}
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl ffmpeg && rm -rf /var/lib/apt/lists/*
COPY --from=whisper-builder /app/build/bin /app/build/bin
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
    JAVBEACONSUBS_DOWNLOAD_MODELS=true \
    JAVBEACONSUBS_GPU_FALLBACK_CPU=true
EXPOSE 8097
VOLUME ["/data", "/models"]
ENTRYPOINT ["/usr/local/bin/javbeaconsubs-entrypoint"]
CMD ["/usr/local/bin/javbeaconsubs", "-config", "/app/config.json"]
