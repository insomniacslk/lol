FROM golang:1.22 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

LABEL BUILD="docker buildx build --platform linux/amd64,linux/arm64 -t insomniacslk/lol -f Dockerfile ."
LABEL RUN="docker run --rm -it insomniacslk/lol"

WORKDIR /app

COPY . .

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o lol .

FROM debian:bookworm-slim

WORKDIR /app

COPY --from=builder /app/lol /app/lol
COPY config.yaml.example /app/config.yaml

ENTRYPOINT ["/app/lol", "-c", "/app/config.yaml"]
