FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
# The version /v1/info and the root page show: the release tag without "v", or main-<sha>.
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /sepia-server .

FROM alpine:3.22
ARG TARGETARCH
# yt-dlp: trailers played on TVs (/v1/videos/{key}/stream). The zipapp on
# python3, not the one-file build: that one unpacks ~80 MB to /tmp on every run
# (an SD card here). The server copies it to /tmp and lets it update itself
# daily: YouTube breaks old releases.
ARG YTDLP_VERSION=2026.10.26
RUN apk add --no-cache ffmpeg ca-certificates tzdata python3 \
 && adduser -D -u 1000 sepia \
 && base=https://github.com/yt-dlp/yt-dlp/releases/download/$YTDLP_VERSION \
 && wget -q -O /tmp/SUMS $base/SHA2-256SUMS \
 && wget -q -O /usr/local/bin/yt-dlp $base/yt-dlp \
 && echo "$(grep ' yt-dlp$' /tmp/SUMS | cut -d' ' -f1)  /usr/local/bin/yt-dlp" | sha256sum -c - \
 && chmod 755 /usr/local/bin/yt-dlp \
 && rm /tmp/SUMS
# The database's dir belongs to sepia (uid 1000, as before the rename): a fresh Docker
# volume on /data takes the image dir's owner, and a root-owned one can't take
# /data/detector.db.
RUN install -d -o sepia -g sepia /data
COPY --from=build /sepia-server /usr/local/bin/sepia-server
USER 1000
ENTRYPOINT ["/usr/local/bin/sepia-server"]
