FROM golang:1.13-alpine as builder

# Force Go to use the cgo based DNS resolver. This is required to ensure DNS
# queries required to connect to linked containers succeed.
ENV GODEBUG netdns=cgo

ADD . /go/src/github.com/lightninglabs/governator

# Install dependencies and build the binaries.
RUN apk add --no-cache --update alpine-sdk \
    git \
    make \
    gcc \
&&  cd /go/src/github.com/lightninglabs/governator \
&&  make \
&&  make install

# Start a new, final image.
FROM alpine as final

# Add bash and ca-certs, for quality of life and SSL-related reasons.
RUN apk --no-cache add \
    bash \
    ca-certificates

# Copy the binaries from the builder image.
COPY --from=builder /go/bin/governator /bin/
COPY --from=builder /go/bin/gvncli /bin/

# Expose governator ports (rpc).
EXPOSE 8465

# Specify the start command and entrypoint as the governator daemon.
ENTRYPOINT ["governator"]
