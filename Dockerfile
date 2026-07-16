FROM golang:1.26.5-alpine AS build-env

# Build phase
RUN apk add bash build-base git

ADD ./ /workspace/northstar
WORKDIR /workspace/northstar

RUN make build

# Lightweight runtime image
FROM alpine
WORKDIR /workspace/northstar
RUN apk add ca-certificates
COPY --from=build-env /workspace/northstar/bin/ /workspace/northstar/bin/

# DNS ports: UDP, TCP, DoT
EXPOSE 53/udp
EXPOSE 53/tcp
EXPOSE 853/tcp

ENTRYPOINT ["/workspace/northstar/bin/northstar"]
