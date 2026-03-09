# Build stage (Ubuntu 24.04 を使用して GLIBC バージョンを合わせる)
FROM ubuntu:24.04 AS builder

ARG LIBDAVE_VERSION=v1.1.0
ARG GO_VERSION=1.24.0

ENV DEBIAN_FRONTEND=noninteractive \
    CGO_ENABLED=1 \
    PKG_CONFIG_PATH=/root/.local/lib/pkgconfig \
    PATH=$PATH:/usr/local/go/bin

WORKDIR /app

# 必要なパッケージと Go のインストール
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    curl \
    git \
    pkg-config \
    unzip \
    && curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz | tar -C /usr/local -xzf - \
    && rm -rf /var/lib/apt/lists/*

# libdave のインストール (FORCE_BUILD=1 を外す)
RUN curl -fsSL -o /tmp/libdave_install.sh https://raw.githubusercontent.com/disgoorg/godave/refs/heads/master/scripts/libdave_install.sh \
    && chmod +x /tmp/libdave_install.sh \
    && NON_INTERACTIVE=1 SHELL=/bin/sh /tmp/libdave_install.sh ${LIBDAVE_VERSION} \
    && rm -f /tmp/libdave_install.sh

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -o asagumo-run .

# Runtime stage (ビルド時と同じ Ubuntu 24.04 を使用)
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/asagumo-run .
COPY --from=builder /root/.local/lib/libdave.so /usr/local/lib/

# ライブラリパスの設定
ENV LD_LIBRARY_PATH=/usr/local/lib
RUN ldconfig

ENV PORT=8000
EXPOSE 8000

CMD ["./asagumo-run"]
