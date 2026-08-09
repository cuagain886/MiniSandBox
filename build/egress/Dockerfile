# syntax=docker/dockerfile:1.12

ARG GO_BUILDER="golang:1.26-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599"
ARG DEBIAN_BASE="debian:bookworm-slim@sha256:abd67ffcfa541b485a3dff59865ab629aa048a6c613e639d36e7456b0b229241"

FROM ${GO_BUILDER} AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/egressd ./cmd/egressd
COPY internal/egressanchor ./internal/egressanchor
COPY internal/egresscontrol ./internal/egresscontrol
COPY internal/egressnft ./internal/egressnft
COPY internal/egresspolicy ./internal/egresspolicy
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/egressd ./cmd/egressd

FROM ${DEBIAN_BASE} AS nft-runtime
ARG DEBIAN_SNAPSHOT="20260803T000000Z"
ARG NFTABLES_VERSION="1.0.6-2+deb12u2"
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN printf 'deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/%s bookworm main\n' "$DEBIAN_SNAPSHOT" > /etc/apt/sources.list \
    && rm -f /etc/apt/sources.list.d/* \
    && apt-get -o Acquire::Check-Valid-Until=false update \
    && apt-get install -y --no-install-recommends "nftables=${NFTABLES_VERSION}" \
    && mkdir -p /rootfs/usr/sbin \
    && cp /usr/sbin/nft /rootfs/usr/sbin/nft \
    && ldd /usr/sbin/nft | awk '$(NF-2) == "=>" { print $(NF-1) } $1 ~ /^\// { print $1 }' \
       | sort -u | xargs -r -I '{}' cp -L --parents '{}' /rootfs \
    && /usr/sbin/nft --version | grep -F "v${NFTABLES_VERSION%%-*}"

FROM scratch
ARG SOURCE_REVISION="unknown"
ARG NFTABLES_VERSION="1.0.6-2+deb12u2"
LABEL org.opencontainers.image.title="MiniSandbox egress namespace anchor" \
      org.opencontainers.image.source="https://github.com/minisandbox/minisandbox" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      minisandbox.io/egress-protocol-version="1" \
      minisandbox.io/egress-rule-schema-version="1" \
      minisandbox.io/nftables-version="${NFTABLES_VERSION}"
COPY --from=nft-runtime /rootfs /
COPY --from=go-build /out/egressd /usr/local/bin/egressd
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/egressd", "bootstrap"]
