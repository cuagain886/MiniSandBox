# syntax=docker/dockerfile:1
FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -o internal/embedded/artifacts/linux_amd64/runnerd ./cmd/runnerd
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -o internal/embedded/artifacts/linux_amd64/sandbox-init ./cmd/sandbox-init
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -o /out/sandboxd ./cmd/sandboxd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sandboxd /usr/local/bin/sandboxd
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sandboxd"]

