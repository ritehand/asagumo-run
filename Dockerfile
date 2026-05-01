ARG GO_VERSION=1.26.0

FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache \
    build-essential \
    ca-certificates \
    git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o bot .

FROM alpine:latest

RUN apk add --no-cache \
    ca-certificates

WORKDIR /app

COPY --from=builder /app/bot .

ENV PORT=8000
EXPOSE 8000

CMD ["./bot"]
