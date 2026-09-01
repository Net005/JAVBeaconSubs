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
ARG REAZONSPEECH_REF=8be654dc9ba8d205759d9d93fe717ae37f321b01
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends bash ca-certificates curl ffmpeg libsndfile1 python3 python3-pip sox util-linux && rm -rf /var/lib/apt/lists/*
RUN python3 -m pip install --no-cache-dir "numpy==1.26.4" && \
    python3 -m pip install --no-cache-dir "numpy==1.26.4" "nemo_toolkit[asr]==2.6.1" "qwen-asr==0.0.6" && \
    python3 -m pip install --no-cache-dir --no-deps "reazonspeech-nemo-asr @ https://github.com/reazon-research/ReazonSpeech/archive/${REAZONSPEECH_REF}.tar.gz#subdirectory=pkg/nemo-asr"
COPY --from=whisper-builder /app/build/bin /app/build/bin
COPY --from=go-builder /out/javbeaconsubs /usr/local/bin/javbeaconsubs
COPY config.docker.json /app/config.json
COPY asr /app/asr
COPY docker/entrypoint.sh /usr/local/bin/javbeaconsubs-entrypoint
RUN chmod 0755 /usr/local/bin/javbeaconsubs-entrypoint /app/asr/reazon_worker.py /app/asr/qwen_pipeline.py && mkdir -p /data/uploads /models /scripts
ENV JAVBEACONSUBS_LISTEN=0.0.0.0:8097 \
    JAVBEACONSUBS_DATABASE_PATH=/data/javbeaconsubs.db \
    JAVBEACONSUBS_UPLOAD_DIR=/data/uploads \
    JAVBEACONSUBS_WHISPER_BINARY=/app/build/bin/whisper-cli \
    JAVBEACONSUBS_WHISPER_MODEL=/models/ggml-large-v3.bin \
    JAVBEACONSUBS_VAD_MODEL=/models/ggml-silero-v6.2.0.bin \
    JAVBEACONSUBS_ASR_BACKEND=qwen \
    JAVBEACONSUBS_ASR_MODE=balanced \
    JAVBEACONSUBS_ASR_PROFILE=jav \
    JAVBEACONSUBS_QWEN_SCRIPT=/app/asr/qwen_pipeline.py \
    JAVBEACONSUBS_QWEN_MODEL=Qwen/Qwen3-ASR-1.7B \
    JAVBEACONSUBS_QWEN_REVISION=7278e1e70fe206f11671096ffdd38061171dd6e5 \
    JAVBEACONSUBS_ALIGNER_MODEL=Qwen/Qwen3-ForcedAligner-0.6B \
    JAVBEACONSUBS_ALIGNER_REVISION=c7cbfc2048c462b0d63a45797104fc9db3ad62b7 \
    JAVBEACONSUBS_REAZON_SCRIPT=/app/asr/reazon_worker.py \
    JAVBEACONSUBS_REAZON_MODEL=reazon-research/reazonspeech-nemo-v2 \
    JAVBEACONSUBS_REAZON_READY_MARKER=/models/reazonspeech/.javbeaconsubs-ready \
    JAVBEACONSUBS_DOWNLOAD_MODELS=true \
    JAVBEACONSUBS_GPU_FALLBACK_CPU=true \
    HF_HOME=/models/huggingface
EXPOSE 8097
VOLUME ["/data", "/models", "/scripts"]
ENTRYPOINT ["/usr/local/bin/javbeaconsubs-entrypoint"]
CMD ["/usr/local/bin/javbeaconsubs", "-config", "/app/config.json"]
