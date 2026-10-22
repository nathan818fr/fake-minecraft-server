# Getting started

This page runs the container image. To run the binary instead, see
[Deployment](deployment.md#without-a-container).

## Run the container image

The image is published to the GitHub Container Registry, for `linux/amd64`
and `linux/arm64`:

```text
ghcr.io/nathan818fr/fake-minecraft-server:v2
```

| Tag      | Points to                                                              |
| -------- | ---------------------------------------------------------------------- |
| `v2.0.0` | That release, never moved                                              |
| `v2`     | The latest v2 release: new features and fixes, never a breaking change |
| `latest` | The latest release, whatever its major version                         |

The image runs as user `65534:65534` (`nobody`): the files it reads, such as
the configuration and favicons, must be readable by that user. With Docker
(or Podman, with the same options):

```shell
docker run -d --name fake-minecraft-server --restart unless-stopped \
  -p 25565:25565 \
  -v "$PWD/config:/config:ro" -e FMS_CONFIG_FILE=/config/config.yaml \
  ghcr.io/nathan818fr/fake-minecraft-server:v2
```

Each setting can also be set by an environment variable, which overrides the
file (see [Configuration](configuration.md#environment-variables)).

## Run with Compose

```yaml
# compose.yaml
services:
  fake-minecraft-server:
    image: ghcr.io/nathan818fr/fake-minecraft-server:v2
    restart: unless-stopped
    ports: ["25565:25565"]
    volumes: ["./config:/config:ro"]
    environment:
      FMS_CONFIG_FILE: /config/config.yaml
    healthcheck:
      test: ["CMD", "/fake-minecraft-server", "healthcheck"]
      interval: 1m
```

```shell
docker compose up -d
docker compose logs -f
```

## Check that it works

Add the address of the server to the server list of Minecraft: it shows your
status, and joining it kicks you with your message. Each connection logs one
line (see [Logs](logs.md)).

## Reload and stop

`SIGHUP` [reloads](configuration.md#reloading) the configuration, without
dropping connections:

```shell
docker kill -s HUP fake-minecraft-server
docker compose kill -s HUP fake-minecraft-server
```

On `SIGTERM` or `SIGINT`, the server stops accepting connections, waits for
the open ones (at most `connections.timeout`, 3 seconds by default), then
exits. A second signal stops it at once.

## Next steps

- [Configuration](configuration.md): statuses, servers, upstreams and rules
- [Examples](../examples): configurations to start from
- [Deployment](deployment.md): without a container, proxies, firewalls and
  resources
