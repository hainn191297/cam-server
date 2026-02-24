FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/go-cam-server ./cmd/server

FROM alpine:3.21

WORKDIR /app

COPY --from=builder /out/go-cam-server /usr/local/bin/go-cam-server
COPY server.docker.yml /app/server.docker.yml

RUN mkdir -p /app/hls /app/storage

EXPOSE 1935 8080

ENTRYPOINT ["/usr/local/bin/go-cam-server", "-config", "/app/server.docker.yml"]
