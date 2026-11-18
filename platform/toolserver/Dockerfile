FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY platform/toolserver/go.mod ./go.mod
RUN go mod download

COPY platform/toolserver/ ./
RUN go test ./... \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/devo-toolserver .

FROM grafana/mcp-grafana:{{grafana_mcp_version}}

COPY --from=builder /out/devo-toolserver /usr/local/bin/devo-toolserver

ENV TOOLSERVER_ADDR=0.0.0.0:8000 \
    TOOLSERVER_HEALTH_URL=http://127.0.0.1:8000/healthz

EXPOSE 8000

HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=12 \
    CMD ["/usr/local/bin/devo-toolserver", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/devo-toolserver"]
