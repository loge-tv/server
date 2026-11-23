FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /loge-server .

FROM alpine:3.22
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && adduser -D -u 1000 loge
COPY --from=build /loge-server /usr/local/bin/loge-server
USER 1000
ENTRYPOINT ["/usr/local/bin/loge-server"]
