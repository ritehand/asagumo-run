# Build stage
FROM golang:1.24-bookworm AS builder

# Install build dependencies
RUN apt-get update && apt-get install -y \
    git \
    curl \
    unzip \
    pkg-config \
    ca-certificates \
    cmake \
    make \
    g++ \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy the installation script
COPY scripts/libdave_install.sh ./scripts/libdave_install.sh
RUN chmod +x ./scripts/libdave_install.sh

# Install libdave (v1.1.0 as requested)
# The script installs to $HOME/.local (which is /root/.local in this container)
# We set SHELL and NON_INTERACTIVE because the script has 'set -u' and expects these.
RUN SHELL=/bin/bash NON_INTERACTIVE=1 ./scripts/libdave_install.sh v1.1.0

# Set environment variables for pkg-config and linker
ENV PKG_CONFIG_PATH=/root/.local/lib/pkgconfig
ENV LD_LIBRARY_PATH=/root/.local/lib

# Download Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build
COPY . .
RUN go build -v -o asagumo-run .

# Runtime stage
FROM debian:bookworm-slim

# Install runtime dependencies
RUN apt-get update && apt-get install -y \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/asagumo-run .

# Copy libdave shared library from the builder stage
COPY --from=builder /root/.local/lib/libdave.so /usr/local/lib/

# Update library cache
RUN ldconfig

# Set the port (default to 8000 as per main.go)
ENV PORT=8000
EXPOSE 8000

# Run the application
CMD ["./asagumo-run"]
