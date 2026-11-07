# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Build metadata, injected at build time. The Docker workflow passes the tag;
# a plain `docker build .` falls back to "dev" rather than a misleading version.
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

# Build the static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/mailer .

# ---- runtime stage ----
FROM alpine:3.20

# ARGs are scoped to the stage that declares them, so the build metadata must be
# re-declared here to be visible to the image labels below.
ARG VERSION=dev
ARG COMMIT=unknown

# CA certificates are required for TLS (IMAPS + Telegram/Discord HTTPS).
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -H -u 10001 mailer

WORKDIR /app
COPY --from=build /out/mailer /usr/local/bin/mailer

# Lets operators confirm which image they are actually running:
#   docker exec mailer mailer version
LABEL org.opencontainers.image.title="mailer" \
      org.opencontainers.image.description="IMAP to Telegram/Discord notification daemon" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

# config.yaml and state.db are mounted/persisted via /app.
USER mailer

ENTRYPOINT ["mailer"]
CMD ["-config", "/app/config.yaml"]
