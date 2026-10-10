# The Go version of mise, which also runs the tests, passed by "mise run image".
ARG GO_VERSION
FROM --platform=$BUILDPLATFORM docker.io/library/golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY . .
ARG TARGETOS TARGETARCH
# The version that the server logs at startup, passed by "mise run image".
ARG VERSION
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/fake-minecraft-server ./cmd/fake-minecraft-server

FROM scratch
COPY --from=build /out/fake-minecraft-server /fake-minecraft-server

LABEL org.opencontainers.image.title="fake-minecraft-server" \
      org.opencontainers.image.description="A fake Minecraft server that answers server list pings and kicks players" \
      org.opencontainers.image.url="https://github.com/nathan818fr/fake-minecraft-server" \
      org.opencontainers.image.source="https://github.com/nathan818fr/fake-minecraft-server" \
      org.opencontainers.image.licenses="MIT"

USER 65534:65534
EXPOSE 25565
ENTRYPOINT ["/fake-minecraft-server"]
