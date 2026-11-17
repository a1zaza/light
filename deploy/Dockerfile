FROM node:24-alpine AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

FROM golang:1.26.11-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY web/fallback/ ./web/fallback/
COPY web/public/ ./web/public/
COPY --from=frontend /src/web/dist/ ./web/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/light ./cmd/light

FROM alpine:3.23
LABEL org.opencontainers.image.title="light" \
      org.opencontainers.image.description="个人 Docker/OCI 镜像仓库" \
      org.opencontainers.image.source="https://github.com/a1zaza/light" \
      org.opencontainers.image.url="https://github.com/a1zaza/light"
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 1000 light && \
    adduser -S -D -H -u 1000 -G light light && \
    mkdir -p /var/lib/light && chown light:light /var/lib/light && chmod 0700 /var/lib/light
COPY --from=backend /out/light /usr/local/bin/light
USER 1000:1000
ENV TZ=Asia/Shanghai
WORKDIR /var/lib/light
EXPOSE 5080
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s --retries=3 CMD ["light", "health", "-url", "http://127.0.0.1:5080/readyz"]
ENTRYPOINT ["/usr/local/bin/light"]
CMD ["serve"]
