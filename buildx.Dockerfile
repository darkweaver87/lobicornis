# syntax=docker/dockerfile:1.4
FROM golang:1.25-alpine AS builder
WORKDIR /usr/local/src/

RUN apk add --no-cache make
COPY . /usr/local/src/
RUN make build

FROM alpine:3.20

RUN apk --no-cache --no-progress add ca-certificates git \
    && rm -rf /var/cache/apk/*

COPY --from=builder /usr/local/src/lobicornis /

ENTRYPOINT ["/lobicornis"]
EXPOSE 80
