# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
RUN CGO_ENABLED=1 go build -tags sqlite_omit_load_extension -trimpath -ldflags="-s -w" -o /out/dbaui ./cmd/dbaui

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && install -d -m 0700 -o 10001 -g 10001 /data
COPY --from=build /out/dbaui /app/dbaui
USER 10001:10001
ENV DBAUI_ADDR=:8080 \
    DBAUI_DATABASE=/data/dbaui.db
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/dbaui", "-healthcheck"]
ENTRYPOINT ["/app/dbaui"]
