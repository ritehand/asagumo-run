# Ubuntu version same as libdave glibc version
ARG UBUNTU_VERSION=24.04
ARG LIBDAVE_VERSION=v1.1.0
ARG GO_VERSION=1.24.0

FROM ubuntu:${UBUNTU_VERSION} AS builder

ARG LIBDAVE_VERSION
ARG GO_VERSION

ENV DEBIAN_FRONTEND=noninteractive \
    CGO_ENABLED=1 \
    PKG_CONFIG_PATH=/root/.local/lib/pkgconfig \
    PATH=$PATH:/usr/local/go/bin

WORKDIR /app

# Install necessary packages and Go
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    curl \
    git \
    pkg-config \
    unzip \
    && curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz | tar -C /usr/local -xzf - \
    && rm -rf /var/lib/apt/lists/*

# Install libdave
RUN mkdir -p /root/.local/include /root/.local/lib/pkgconfig \
    && curl -fsSL -o /tmp/libdave.zip "https://github.com/discord/libdave/releases/download/${LIBDAVE_VERSION}/cpp/libdave-Linux-X64-boringssl.zip" \
    && unzip -j /tmp/libdave.zip "include/dave/dave.h" -d /root/.local/include \
    && unzip -j /tmp/libdave.zip "lib/libdave.so" -d /root/.local/lib \
    && rm /tmp/libdave.zip \
    && cat <<EOF > /root/.local/lib/pkgconfig/dave.pc
prefix=/root/.local
exec_prefix=\${prefix}
libdir=\${exec_prefix}/lib
includedir=\${prefix}/include

Name: dave
Description: Discord Audio & Video End-to-End Encryption (DAVE) Protocol
Version: ${LIBDAVE_VERSION}
Libs: -L\${libdir} -ldave -Wl,-rpath,\${libdir}
Cflags: -I\${includedir}
EOF

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -o bot .

FROM ubuntu:${UBUNTU_VERSION}

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/bot .
COPY --from=builder /root/.local/lib/libdave.so /usr/local/lib/

# Set library path
ENV LD_LIBRARY_PATH=/usr/local/lib
RUN ldconfig

ENV PORT=8000
EXPOSE 8000

CMD ["./bot"]
