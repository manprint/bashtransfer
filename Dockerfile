# Static build (CGO_ENABLED=0) and shell-free runtime. Images pinned by tag and digest (D14).
# Builder runs on the build platform and cross-compiles for TARGETOS/TARGETARCH:
# the multi-architecture image (amd64, arm64) needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/bashtransfer ./cmd/bashtransfer

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /out/bashtransfer /bashtransfer
USER nonroot:nonroot
EXPOSE 8080
# Runtime has no shell or curl: the binary checks itself (GET / on BASHT_LISTEN, loopback only).
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/bashtransfer", "-healthcheck"]
ENTRYPOINT ["/bashtransfer"]
