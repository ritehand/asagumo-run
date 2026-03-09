# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies for Alpine
# libdave build needs: git, curl, unzip, pkgconfig, cmake, make, g++, ninja, zip, perl, nasm
RUN apk add --no-cache \
    git \
    curl \
    unzip \
    pkgconfig \
    cmake \
    make \
    g++ \
    ninja \
    zip \
    perl \
    nasm \
    ca-certificates \
    linux-headers

WORKDIR /app

# Copy the installation script
COPY scripts/libdave_install.sh ./scripts/libdave_install.sh
RUN chmod +x ./scripts/libdave_install.sh

# Install libdave (v1.1.0)
# Force build from source for Alpine (musl compatibility)
ENV VCPKG_FORCE_SYSTEM_BINARIES=1
ENV CC=/usr/bin/gcc
ENV CXX=/usr/bin/g++
ENV CXXFLAGS="-Wno-error=maybe-uninitialized"
RUN SHELL=/bin/sh NON_INTERACTIVE=1 FORCE_BUILD=1 ./scripts/libdave_install.sh v1.1.0

# Set environment variables for pkg-config and linker
# libdave_install.sh installs to $HOME/.local (which is /root/.local)
ENV PKG_CONFIG_PATH=/root/.local/lib/pkgconfig
ENV LD_LIBRARY_PATH=/root/.local/lib

# Download Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build
COPY . .
RUN go build -v -o asagumo-run .

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    libstdc++ \
    libgcc

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/asagumo-run .

# Copy libdave shared library from the builder stage
COPY --from=builder /root/.local/lib/libdave.so /usr/local/lib/

# Update library cache (Alpine doesn't use ldconfig in the same way, but placing in /usr/local/lib is standard)
# We might need to set LD_LIBRARY_PATH in runtime too
ENV LD_LIBRARY_PATH=/usr/local/lib

# Set the port
ENV PORT=8000
EXPOSE 8000

# Run the application
CMD ["./asagumo-run"]
