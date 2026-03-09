# builder stage
FROM golang:1.24-bookworm AS builder

ARG LIBDAVE_VERSION=v1.1.0

ENV DEBIAN_FRONTEND=noninteractive \
    CGO_ENABLED=1 \
    PKG_CONFIG_PATH=/root/.local/lib/pkgconfig \
    SHELL=/bin/sh

WORKDIR /app

# Install build dependencies
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        cmake \
        curl \
        git \
        make \
        nasm \
        pkg-config \
        unzip \
        zip \
    && rm -rf /var/lib/apt/lists/*

# Install libdave using the remote script
RUN curl -fsSL -o /tmp/libdave_install.sh https://raw.githubusercontent.com/disgoorg/godave/refs/heads/master/scripts/libdave_install.sh \
    && chmod +x /tmp/libdave_install.sh \
    && FORCE_BUILD=1 NON_INTERACTIVE=1 /tmp/libdave_install.sh ${LIBDAVE_VERSION} \
    && rm -f /tmp/libdave_install.sh

# Download Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build
COPY . .
RUN go build -trimpath -o asagumo-run .

# Runtime stage (Switching to debian-slim to match the builder's glibc)
FROM debian:bookworm-slim

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libssl-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/asagumo-run .

# Copy libdave shared library from the builder stage
COPY --from=builder /root/.local/lib/libdave.so /usr/local/lib/

# Update library cache
RUN ldconfig

ENV LD_LIBRARY_PATH=/usr/local/lib
ENV PORT=8000
EXPOSE 8000

# Run the application
CMD ["./asagumo-run"]
