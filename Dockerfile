# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src

ARG VERSION=dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/m3u8-downloader .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata su-exec \
    && addgroup -g 1000 app \
    && adduser -D -u 1000 -G app app

COPY --from=build /out/m3u8-downloader /usr/local/bin/m3u8-downloader
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENV PORT=8080 \
    DOWNLOAD_DIR=/downloads \
    CONFIG_PATH=/config/config.json \
    TZ=Asia/Shanghai

EXPOSE 8080
VOLUME ["/downloads", "/config"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- "http://127.0.0.1:${PORT}/healthz" || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["--serve"]
