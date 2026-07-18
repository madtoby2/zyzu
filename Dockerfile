FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o zyzu ./cmd/zyzu/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata ffmpeg
WORKDIR /app
COPY --from=builder /build/zyzu .
EXPOSE 8080
VOLUME ["/app/data", "/app/videos"]
ENV ZYZU_DB=/app/data/zyzu.db
ENV ZYZU_VIDEO_DIR=/app/videos
ENTRYPOINT ["./zyzu"]
