FROM alpine:latest

ARG TARGETPLATFORM

RUN apk --no-cache add ca-certificates tzdata

COPY ${TARGETPLATFORM}/github-release-monitor /usr/bin/github-release-monitor

ENTRYPOINT ["/usr/bin/github-release-monitor"]