FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod ./
COPY . .

RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/bot ./cmd/bot

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata su-exec && \
    adduser -D -H -u 10001 appuser

WORKDIR /app

COPY --from=builder /out/bot /app/bot
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN chmod +x /usr/local/bin/docker-entrypoint.sh && \
    mkdir -p /data && chown -R appuser:appuser /app /data

ENV SQLITE_PATH=/data/bot.db

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/app/bot"]
