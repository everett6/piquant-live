FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/

ARG PIQUANT_VERSION=cloud
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${PIQUANT_VERSION}" -o /out/piquant ./cmd/piquant

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl bash
WORKDIR /app

COPY --from=builder /out/piquant /usr/local/bin/piquant
COPY configs/ /app/configs/
COPY scripts/cloud-entrypoint.sh /app/cloud-entrypoint.sh
RUN chmod +x /app/cloud-entrypoint.sh && mkdir -p /data

ENV HOME=/data
ENV PIQUANT_DATA_DIR=/data/piquant-data
ENV PIQUANT_LOG_DIR=/data/logs
ENV PIQUANT_AUTO_SETUP=1

ENTRYPOINT ["/app/cloud-entrypoint.sh"]
