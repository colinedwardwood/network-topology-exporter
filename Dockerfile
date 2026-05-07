# syntax=docker/dockerfile:1.7

# ─── builder ────────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /src

RUN apk add --no-cache git make

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/colinedwardwood/network-topology-exporter/internal/version.Version=${VERSION} \
      -X github.com/colinedwardwood/network-topology-exporter/internal/version.Commit=${COMMIT} \
      -X github.com/colinedwardwood/network-topology-exporter/internal/version.BuildDate=${DATE}" \
    -o /out/topology-exporter ./cmd/topology-exporter

# ─── runtime ────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="network-topology-exporter"
LABEL org.opencontainers.image.description="Network topology discovery exporter for Prometheus / Loki / OTLP."
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.source="https://github.com/colinedwardwood/network-topology-exporter"

USER nonroot:nonroot

COPY --from=builder /out/topology-exporter /usr/local/bin/topology-exporter
COPY config/example.yaml /etc/topology-exporter/config.yaml

EXPOSE 9100

ENTRYPOINT ["/usr/local/bin/topology-exporter"]
CMD ["--config.file=/etc/topology-exporter/config.yaml", "--web.listen-address=:9100"]
