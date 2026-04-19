FROM golang:1.25-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN VERSION=$(git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD 2>/dev/null || echo "unknown") && \
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags="-w -s -X main.Version=${VERSION}" \
  -o /lattice-api .

# ---

FROM alpine:3.19

RUN apk add --no-cache ca-certificates curl docker-cli docker-cli-compose

COPY --from=builder /lattice-api /usr/local/bin/lattice-api

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8000/healthcheck || exit 1

ENTRYPOINT ["lattice-api"]
