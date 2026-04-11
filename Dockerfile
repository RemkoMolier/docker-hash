FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY docker-hash /usr/local/bin/docker-hash

ENTRYPOINT ["docker-hash"]
