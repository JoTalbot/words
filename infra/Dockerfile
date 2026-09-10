# syntax=docker/dockerfile:1
# Word Arena — M0 game service image (static binary, runs as nonroot).
# Build context must be the repository root:
#   docker build -f infra/Dockerfile -t wordarena-game .

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/wordarena ./cmd/game

FROM alpine:3.20
RUN addgroup -S wordarena && adduser -S wordarena -G wordarena && \
    mkdir -p /var/log/wordarena && chown wordarena:wordarena /var/log/wordarena
COPY --from=build /out/wordarena /usr/local/bin/wordarena
USER wordarena
EXPOSE 8080
ENTRYPOINT ["wordarena"]
