# Upgrading from v1

v2 is a rewrite in Go. Its configuration is new: a v1 setup needs the changes
below. Until then, if neither `FMS_CONFIG_FILE` nor any other `FMS_` variable
is set, v2 reads the v1 variables, ignores the values that v1 ignored, such as
a favicon it cannot read, and logs a warning with the `FMS_` variables that
replace them. Everything v1 did, v2 does, with the same image name, except
`LISTEN_BACKLOG`.

## The image

Use the `v2` tag: `ghcr.io/nathan818fr/fake-minecraft-server:v2`. The
`latest` tag now points to v2: to stay on v1, use `v1.1.0`.

- The image is built `FROM scratch`: it holds the binary alone, without
  Node.js nor a shell.
- It runs as user `65534:65534` (`nobody`), instead of `node`: files mounted
  into the container, such as a favicon, must be readable by that user.
- `.env` files are no longer read: set variables with `-e`, `environment:` in
  Compose, or `Environment=` with systemd. `--env-file` keeps quotes as part
  of the values.
- Building from source requires Go instead of Node.js (see
  [Deployment](deployment.md#build-from-source)).

## Variables

Variables now start with `FMS_` and follow the keys of the
[configuration file](configuration.md), which v2 adds. The status and kick
message of v1 are the ones of the `default` server, set in the file or with
`FMS_SERVERS_DEFAULT`:

| v1                           | v2                                        |
| ---------------------------- | ----------------------------------------- |
| `LISTEN_HOST`, `LISTEN_PORT` | `FMS_LISTEN=host:port`                    |
| `LISTEN_BACKLOG`             | Removed                                   |
| `MOTD`                       | `servers.default.status.description`      |
| `FAVICON`                    | `servers.default.status.favicon`          |
| `MAX_PLAYERS`                | `servers.default.status.players.max`      |
| `ONLINE_PLAYERS`             | `servers.default.status.players.online`   |
| `PROTOCOL_NAME`              | `servers.default.status.version.name`     |
| `PROTOCOL_VERSION`           | `servers.default.status.version.protocol` |
| `KICK_MESSAGE`               | `servers.default.kick`                    |

For example, this v1 setup:

```shell
docker run -p 25565:25565 \
  -e MOTD="§eWelcome to My Server!" \
  -e PROTOCOL_NAME="§cMaintenance" \
  -e KICK_MESSAGE="§cSorry, come back later." \
  ghcr.io/nathan818fr/fake-minecraft-server:v1.1.0
```

becomes, with a variable:

```shell
docker run -p 25565:25565 \
  -e FMS_SERVERS_DEFAULT='{ status: { description: "§eWelcome to My Server!", version: { name: "§cMaintenance", protocol: 0 }, players: { max: 0, online: 0 } }, kick: "§cSorry, come back later." }' \
  ghcr.io/nathan818fr/fake-minecraft-server:v2
```

or, with a configuration file:

```yaml
servers:
  default:
    status:
      description: "§eWelcome to My Server!"
      version: { name: "§cMaintenance", protocol: 0 }
      players: { max: 0, online: 0 }
    kick: "§cSorry, come back later."
```

## Behavior changes

- **Configuration mistakes stop the server**, instead of falling back to
  defaults: an unknown key, an invalid value, or a favicon that cannot be
  read or is not a 64×64 PNG, which clients ignore. Every mistake is reported
  at once, named after its setting.
- **Compatible by default.** v1 sent protocol version `0` unless set, so
  clients always showed the server as incompatible, with the version name in
  red. v2 reports the protocol version of each client, so that the server
  shows its player counts, except for statuses with a protocol version lower
  than 4, such as `0`, `1` or `-1`, which still show their version name in
  red. To send the version of the status as written, set
  [`client_protocol: never`](configuration.md#client-protocol).
- **Default status.** Without configuration, the server list shows
  `§cNot available`, like the kick message, instead of `§eHello World!`.
- **Logs** are one line per connection, in text or JSON, instead of several
  free-form lines: log parsers and alerts need updating (see [Logs](logs.md)).
- **Shorter timeouts.** Clients must send their handshake within 1 second
  (`connections.handshake_timeout`, 2 seconds in v1), and connections last at
  most 3 seconds (`connections.timeout`). Real clients send their handshake as
  soon as they connect.
- **Pings are answered like vanilla servers:** the server waits for the ping
  of the client and echoes it, which status tools such as mcstatus check. v1
  answered with a fixed value. Statuses end with `"fakeMinecraftServer":true`,
  so that tools can tell this server from real ones, unless
  `no_software_signature: true`.
