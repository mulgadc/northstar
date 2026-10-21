# Base images are pinned by digest so a rebuild of the same commit gets the
# same bytes; Dependabot bumps the tag and digest together.
FROM golang:1.27.3-alpine3.24@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build-env

# Build phase
RUN apk add bash build-base git

ADD ./ /workspace/northstar
WORKDIR /workspace/northstar

RUN make build

# Lightweight runtime image
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
WORKDIR /workspace/northstar
RUN apk add ca-certificates
COPY --from=build-env /workspace/northstar/bin/ /workspace/northstar/bin/

# DNS ports: UDP, TCP, DoT
EXPOSE 53/udp
EXPOSE 53/tcp
EXPOSE 853/tcp

ENTRYPOINT ["/workspace/northstar/bin/northstar"]
