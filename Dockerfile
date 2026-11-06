# Build stage
FROM golang:1.27.2-alpine AS builder

RUN apk add --no-cache build-base git
WORKDIR /src

# Salin go.mod DAN go.sum
COPY go.mod go.sum ./

# Download dan verifikasi dependencies
RUN go mod download && go mod verify

COPY main.go ./

# Compile aplikasi
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/gateway ./main.go


# Runtime stage
FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 gateway \
    && adduser -S -D -H -u 10001 -G gateway gateway \
    && mkdir -p /data \
    && chown -R gateway:gateway /data

WORKDIR /app

COPY --from=builder /out/gateway /app/gateway

ENV PORT=8080 DATA_DIR=/data

EXPOSE 8080

USER 10001:10001

ENTRYPOINT ["/app/gateway"]
