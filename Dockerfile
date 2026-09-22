# Build the payer.
#
#   docker build -t x402-celestia-rpc-payer .
#
# The build context is this directory. The module has no private dependency,
# so the build needs no credentials.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Copy the module files first, so that a change of the source does not read
# the dependencies again.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# "version" prints this. Pass it with: docker build --build-arg VERSION=v0.1.0 .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/x402-celestia-rpc-payer ./cmd/x402-celestia-rpc-payer

FROM alpine:3.24
RUN apk add --no-cache ca-certificates \
 && adduser -D -u 10001 payer
USER payer

COPY --from=build /out/x402-celestia-rpc-payer /usr/local/bin/x402-celestia-rpc-payer

# WARNING: The mnemonic file holds the key of a wallet that has money. Mount
# it read-only, and give it the file mode 0600 on the host.
VOLUME ["/etc/x402-celestia-rpc-payer"]
EXPOSE 26659

ENTRYPOINT ["/usr/local/bin/x402-celestia-rpc-payer"]
CMD ["-config", "/etc/x402-celestia-rpc-payer/config.yaml", "serve"]
