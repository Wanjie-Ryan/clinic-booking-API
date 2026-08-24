# Stage 1: build a static binary
FROM golang:1.25-bookworm AS build

WORKDIR /app

# Dependencies first so this layer is cached across builds that only change
# application code.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO disabled so the binary has no C library dependency, which is what lets
# the final stage be a minimal image instead of needing glibc-compatible
# build tools.
RUN CGO_ENABLED=0 GOOS=linux go build -o application .

# Stage 2: runtime
FROM debian:12-slim

WORKDIR /app

RUN groupadd -r appuser && useradd -r -g appuser appuser

# ca-certificates for outbound TLS (Uptrace exporter, MySQL/Redis if they're
# ever TLS-terminated); tzdata so CLINIC_TIMEZONE resolves to a real location.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build --chown=appuser:appuser /app/application .
COPY --from=build --chown=appuser:appuser /app/migrations ./migrations

USER appuser

EXPOSE 8080

ENTRYPOINT ["/app/application"]
