# Build statico (CGO_ENABLED=0) e runtime senza shell. Immagini pinnate per tag e digest (D14).
FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bashtransfer ./cmd/bashtransfer

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /out/bashtransfer /bashtransfer
USER nonroot:nonroot
EXPOSE 8080
# Runtime senza shell né curl: il binario si controlla da solo (GET / su BASHT_LISTEN, solo loopback).
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/bashtransfer", "-healthcheck"]
ENTRYPOINT ["/bashtransfer"]
