# Built by "mise run image", from the binaries of GoReleaser in dist/.
FROM scratch
ARG TARGETOS TARGETARCH
COPY dist/fake-minecraft-server_${TARGETOS}_${TARGETARCH}_*/fake-minecraft-server /fake-minecraft-server

LABEL org.opencontainers.image.title="fake-minecraft-server" \
      org.opencontainers.image.description="A fake Minecraft server that answers server list pings and kicks players" \
      org.opencontainers.image.url="https://github.com/nathan818fr/fake-minecraft-server" \
      org.opencontainers.image.source="https://github.com/nathan818fr/fake-minecraft-server" \
      org.opencontainers.image.licenses="MIT"

USER 65534:65534
EXPOSE 25565
ENTRYPOINT ["/fake-minecraft-server"]
