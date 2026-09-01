ARG GO_VERSION=1.26.0
ARG NODE_VERSION=22

FROM node:${NODE_VERSION}-alpine AS frontend

WORKDIR /app/frontend

COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci

COPY frontend/ ./
RUN npm run build

FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=frontend /app/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -o bot .

FROM alpine:latest

RUN apk add --no-cache \
    ca-certificates

WORKDIR /app

COPY --from=builder /app/bot .

ENV PORT=8000
EXPOSE 8000

CMD ["./bot"]
