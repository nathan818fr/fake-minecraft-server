# Configuration

The server reads its settings from a YAML file, and from environment
variables, which override it. Every setting is optional: without any, the
server listens on port 25565, shows `Not available` in the server list, and
kicks players with the same message.

The configuration is strict. An unknown key, an invalid value, a favicon that
is not a 64×64 PNG, or a rule condition that cannot work stops the server at
startup, or leaves the running configuration in place on a
[reload](#reloading), with every mistake reported at once and named after its
setting, such as `connections.max` or `rules.2.when`.
`fake-minecraft-server check` checks a configuration without starting the
server.

- [Configuration file](#configuration-file)
- [Environment variables](#environment-variables)
- [Settings](#settings)
- [Listeners](#listeners)
- [Servers](#servers)
  - [Statuses](#statuses)
  - [Client protocol](#client-protocol)
- [Upstreams](#upstreams)
- [Rules](#rules)
- [Scripts](#scripts)
- [Value formats](#value-formats)
- [Reloading](#reloading)

## Configuration file

The `FMS_CONFIG_FILE` environment variable holds the path of the file. For
example:

```yaml
upstreams:
  # A real server, polled for its status.
  lobby:
    address: 10.0.0.5:25577
    handshake: { host: play.example.com }

servers:
  # The server of the clients that no rule matches.
  default:
    status:
      description: "§cMaintenance, back soon"
      version: { name: "§cMaintenance", protocol: -1 }
    kick: "§cWe are under maintenance, back soon!"

  # Shows the status of the lobby upstream, which has its ID, while it
  # answers, and its own while it does not.
  lobby:
    status: { description: "§eThe lobby is restarting" }
    kick: "§eThe lobby is restarting, try again in a minute."

rules:
  - host: [.example.com]
    server: lobby
```

Paths in the file, such as favicons, are relative to the file.

Editors that use the YAML language server, such as VS Code with its YAML
extension, complete and check the file with its JSON Schema, given by its first
line:

```yaml
# yaml-language-server: $schema=https://github.com/nathan818fr/fake-minecraft-server/releases/latest/download/config.schema.json
```

## Environment variables

Each setting has an environment variable, which overrides the file: `FMS_`,
then its key in uppercase, with `_` in place of `.`, such as
`FMS_CONNECTIONS_MAX_PER_IP` for `connections.max_per_ip`. Variables take the
values of the file, except that:

- Text is used as is, without quotes: `FMS_LOG_LEVEL=debug`.
- Lists replace the list of the file, in JSON or YAML flow style
  (`FMS_RULES='[{ host: [play.example.com], server: lobby }]'`); see
  [Listeners](deployment.md#listeners) for `FMS_LISTEN`.
- Each upstream and server has a variable of its own, named after its ID:
  `FMS_UPSTREAMS_LOBBY`, `FMS_SERVERS_DEFAULT`. It holds the options to change,
  in JSON or YAML flow style, merged into the entry of the file with the same
  ID, which it creates if needed. The options it sets replace the ones of the
  file, the others are kept, and the status of a server is merged field by
  field, at any depth, where `null` removes a field, as in a
  [JSON merge patch](https://www.rfc-editor.org/rfc/rfc7396).

```shell
FMS_CONNECTIONS_MAX=20000
FMS_LOG_FORMAT=json
FMS_UPSTREAMS_LOBBY='{ address: 10.0.0.5:25577, interval: 30s }'
FMS_SERVERS_DEFAULT='{"status":{"players":{"online":42}},"kick":"§cBack soon"}'
```

Empty variables are ignored, and paths in variables, such as a favicon in
`FMS_SERVERS_DEFAULT`, are relative to the working directory.

## Settings

Settings with a dot are nested, such as `connections.max`:

```yaml
connections:
  max: 20000
  max_per_ip: 4
log:
  format: json
```

| Setting                         | Description                                                                                                                           | Default                     |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | --------------------------- |
| `listen`                        | The addresses to listen on, each one alone or with its options (see [Listeners](#listeners))                                          | `[":25565"]`                |
| `connections.timeout`           | The maximum lifetime of a connection                                                                                                  | `3s`                        |
| `connections.handshake_timeout` | The time that a client has to send its handshake, and the request that follows it, after it connects (at most `connections.timeout`)  | `1s`                        |
| `connections.max`               | The maximum number of concurrent connections (`0`: unlimited)                                                                         | `10000`                     |
| `connections.max_per_ip`        | The maximum number of concurrent connections per IPv4 address or IPv6 /64 (`0`: unlimited)                                            | `8`                         |
| `connections.rate_limit_per_ip` | The maximum number of new connections per second per IPv4 address or IPv6 /64, in bursts of up to twice as many (`0`: unlimited)      | `10`                        |
| `log.format`                    | `text`, for people, or `json`, one object per line, for log collectors                                                                | `text`                      |
| `log.level`                     | The minimum level logged: `debug` (also every poll of the upstreams), `info`, `warn` (without the line of each connection) or `error` | `info`                      |
| `log.rate_limit`                | The maximum number of lines logged per second about connections (`0`: unlimited)                                                      | `1000`                      |
| `no_software_signature`         | Whether to leave out `"fakeMinecraftServer":true`, which statuses end with so that tools can tell this server from real ones          | `false`                     |
| `upstreams`                     | The real servers polled for their status, by ID (see [Upstreams](#upstreams))                                                         | None                        |
| `servers`                       | The servers that clients see, by ID (see [Servers](#servers))                                                                         | The `default` server        |
| `rules`                         | Which server each client sees (see [Rules](#rules))                                                                                   | Every client sees `default` |
| `script`                        | Functions and constants written in Starlark, for conditions and hooks (see [Scripts](#scripts))                                       | None                        |
| `script_file`                   | The path of a file that holds the script instead                                                                                      | None                        |

`connections.rate_limit_per_ip` bounds the new connections of each address,
but the server still accepts them: see
[Deployment](deployment.md#rate-limiting-new-connections) to withstand floods.

## Listeners

`listen` takes one or more addresses, each one alone, or with its options:

```yaml
listen:
  - ":25565"
  - { address: ":25566", proxy_protocol: true, trusted_proxies: [10.0.0.0/8] }
```

| Option            | Description                                                                                                 | Default                                 |
| ----------------- | ----------------------------------------------------------------------------------------------------------- | --------------------------------------- |
| `address`         | `host:port` (an empty host listens on every interface, IPv4 and IPv6), or `unix:path` for a Unix socket     | Required                                |
| `proxy_protocol`  | Whether connections start with a PROXY protocol header (see [Behind a proxy](deployment.md#behind-a-proxy)) | `false`                                 |
| `trusted_proxies` | For TCP listeners with `proxy_protocol`: the addresses or CIDR prefixes allowed to send a header            | Required with `proxy_protocol` over TCP |

[Deployment](deployment.md#listeners) has more examples, and covers Unix
sockets and proxies.

## Servers

A server is what clients see: the status in their server list, and what
happens when they try to join. Servers are listed by [ID](#value-formats), so
that each one has a variable:

```yaml
servers:
  default:
    status:
      description: "§6Example Network"
      players: { max: 100, online: 0 }
    kick: "§6Opening on Saturday!"
```

| Option            | Description                                                                                                               | Default                                |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------- | -------------------------------------- |
| `status`          | The status shown in the server list (see [Statuses](#statuses))                                                           | `§cNot available`, with no players     |
| `upstream`        | The ID of the upstream whose status is shown in place of `status` while it answers, or `""` for none                      | The upstream with the ID of the server |
| `client_protocol` | When to report the protocol version of each client: `auto`, `always` or `never` (see [Client protocol](#client-protocol)) | `auto`                                 |
| `kick`            | The message sent to players who try to join (see [Text](#text))                                                           | `§cNot available`                      |
| `ping_script`     | A Starlark expression that can answer status requests in place of the status (see [Hooks](scripting.md#hooks))            | None                                   |
| `join_script`     | A Starlark expression that can answer players in place of the kick message (see [Hooks](scripting.md#hooks))              | None                                   |

Each server answers on its own, never with the settings of another one: the
options it does not set take their built-in default, the same for every
server. The `default` server always exists: the [rules](#rules) end with it,
for the clients that no rule matches. An upstream without a server of the
same ID makes one, which shows that upstream, with the built-in status while
it is down.

### Statuses

A status is written in YAML with the fields of the
[status JSON](https://minecraft.wiki/w/Java_Edition_protocol/Server_List_Ping#Status_Response)
of Minecraft servers:

```yaml
status:
  description:
    text: "Example Network\n"
    color: gold
    extra: [{ text: "Survival, creative and minigames", color: gray }]
  favicon: icon.png
  version: { name: "1.8+", protocol: 47 }
  players:
    max: 500
    online: 0
    sample:
      - { name: "§eDiscord: §fdiscord.example.com", id: 00000000-0000-0000-0000-000000000000 }
  enforcesSecureChat: true
```

| Field                | Description                                                                                                                                                                                          |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `description`        | The message of the day: text, or a [text component](#text), on up to two lines. Clients before 1.7 show its first line only                                                                          |
| `favicon`            | The icon: the path of a 64×64 PNG file, or a data URI of one (`data:image/png;base64,...`)                                                                                                           |
| `version.name`       | The version name, which clients show in red, in place of the player counts, when they see the server as incompatible                                                                                 |
| `version.protocol`   | The protocol version (see [Client protocol](#client-protocol))                                                                                                                                       |
| `players.max`        | The maximum number of players                                                                                                                                                                        |
| `players.online`     | The number of players online                                                                                                                                                                         |
| `players.sample`     | The lines shown when hovering over the player counts, each with a `name` (text with `§` codes) and an `id` (a UUID), followed by `... and N more ...` when there are fewer lines than players online |
| `enforcesSecureChat` | Whether the server requires signed chat messages (1.19.1+)                                                                                                                                           |
| `previewsChat`       | Whether the server previews chat messages as players type them (1.19 to 1.19.2)                                                                                                                      |

Every field is sent as is, including the ones of mods, such as `forgeData`,
and the ones that future versions of Minecraft will add. The server only
checks the types of `version.name` (text), and of `version.protocol`,
`players.max` and `players.online` (integers). A status is at most 32,767
characters of JSON, favicon included, and clients ignore favicons of other
sizes or formats, so both are startup errors.

### Client protocol

Clients show a server as incompatible, with its version name in red in place
of the player counts, when its protocol version differs from theirs.
`client_protocol` tells when to report the protocol version of each client in
place of the one of the status:

| Value    | Behavior                                                                                                                                           |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `auto`   | Report the version of each client, unless the status has a version lower than 4 (the one of 1.7.2), such as the `-1` or `1` of maintenance plugins |
| `always` | Always report the version of each client: every client sees the server as compatible                                                               |
| `never`  | Send the status as written: clients on other versions see the server as incompatible                                                               |

With `auto`, a status such as `version: { name: "§cMaintenance", protocol: -1 }`
shows `Maintenance` in red on every client, and any other status shows the
player counts. It applies to the statuses of upstreams too.

## Upstreams

An upstream is a real server, polled like a client pings it, to show its
status in place of the status of the servers that use it, while it answers.
Each one is polled once, even if no rule uses it, as scripts can read any of
them.

```yaml
upstreams:
  lobby:
    address: 10.0.0.5:25577
    handshake: { host: play.example.com }
    interval: 30s
```

| Option               | Description                                                                                                                               | Default                                                 |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| `address`            | `host` or `host:port` of the server, IPv6 addresses in brackets. Without a port, its SRV record is followed at each poll, like clients do | Required                                                |
| `handshake.host`     | The host name sent in the handshake, for proxies that route by host name, such as BungeeCord and Velocity                                 | The host of `address`, as written                       |
| `handshake.port`     | The port sent in the handshake                                                                                                            | The port of `address`, or `25565`                       |
| `handshake.protocol` | The protocol version sent in the handshake; `-1` asks the server for its own                                                              | `-1`                                                    |
| `interval`           | The time between two polls                                                                                                                | `10s`                                                   |
| `retry_interval`     | The time between two polls after a failed one                                                                                             | `10s`, or `interval` if shorter                         |
| `timeout`            | The maximum duration of a poll                                                                                                            | `5s`, or `interval` if shorter                          |
| `max_age`            | How long the last status stays in use while polls fail, at least `interval + timeout`                                                     | `3` times `interval`, or `interval + timeout` if longer |

The server logs `upstream available` when an upstream answers, and
`upstream unavailable` when its first poll fails or its last status expires;
the other polls are logged at the `debug` level.

A server uses the upstream with its ID, another one with `upstream: lobby`,
or none with `upstream: ""`. To show a different server while an upstream is
down, rather than the status of the same server, use [rules](rules.md#fallbacks).

## Rules

Rules tell which server each client sees: the first rule that matches
answers, and the `default` server answers the clients that none matches.

```yaml
rules:
  - name: creative
    host: [creative.example.com]
    server: creative
  - name: modded
    when: '"FML" in client.host_extra'
    server: modded
```

| Option   | Description                                                                                             | Default           |
| -------- | ------------------------------------------------------------------------------------------------------- | ----------------- |
| `server` | The ID of the server that clients see                                                                   | Required          |
| `host`   | The hosts that clients connected to, one of which must match (see [Rules](rules.md#hosts))              | Any host          |
| `when`   | A condition written in Starlark (see [Rules](rules.md#conditions))                                      | Any client        |
| `name`   | The name of the rule in logs: lowercase letters, digits and underscores, not only digits, nor `default` | Its index, from 0 |

[Rules](rules.md) covers how rules apply, hosts, conditions and recipes.

## Scripts

`script` holds functions and constants written in
[Starlark](https://github.com/google/starlark-go/blob/master/doc/spec.md), which
conditions and hooks can use, and `script_file` the path of a file that holds
them instead, relative to the configuration file. Only one of them can be
set; `FMS_SCRIPT` and `FMS_SCRIPT_FILE` replace either, but cannot be set
together.

```yaml
script: |
  OFFICE = ["198.51.100.0/24", "2001:db8:100::/48"]

  def from_office(client):
      return fms.in_network(client.ip, *OFFICE)
```

[Scripting](scripting.md) describes the language, what scripts see, and how
they are checked.

## Value formats

| Kind      | Format                                                                                                         |
| --------- | -------------------------------------------------------------------------------------------------------------- |
| Durations | A number and a unit among `ms`, `s`, `m` and `h`, such as `500ms`, `3s` or `1m30s`. Durations must be positive |
| Counts    | Integers from `0` to `2147483647`. For limits, `0` means unlimited                                             |
| IDs       | Lowercase letters, digits and underscores, such as `lobby` or `survival_2`                                     |

### Text

Text shown to players, such as the message of the day and kick messages, is
either:

- Text with [formatting codes](https://minecraft.wiki/w/Formatting_codes),
  such as `"§cRed §lbold"`. In double-quoted YAML strings, `\n` is a line
  break.
- A [text component](https://minecraft.wiki/w/Text_component_format), written
  in YAML, with any of the 16 named colors or hex colors such as `"#ff8800"`:

  ```yaml
  kick:
    text: "We are under maintenance.\n\n"
    color: red
    extra:
      - { text: "News on ", color: gray }
      - { text: "status.example.com", color: aqua, underlined: true }
  ```

Text that starts with `{` is read as JSON, which must be valid. Clients before
1.7 only show legacy text: text components are converted to `§` codes, and hex
colors to the closest of the 16 colors.

### Quoting

YAML reads some values as something else unless they are quoted: IPv6
addresses (`"[2001:db8::1]:25565"`), host patterns that start with `*`
(`"*.example.com"`), and version names that look like numbers (`"1.20"`).

## Reloading

On `SIGHUP`, the server reads its configuration again and applies it without
dropping connections. If anything is wrong, it logs every mistake
(`reload failed`) and keeps the running configuration.

```shell
kill -HUP "$(pidof fake-minecraft-server)"
```

- Connections already open end with the configuration they started with. New
  limits count them, but never close them.
- Upstreams whose options did not change keep polling. The others poll again
  at once, and keep their last status until they get a new one, or until it
  is older than `max_age`: changing the address of an upstream does not make
  its servers fall back to their own status.
- Listeners whose address is written the same stay open, with their new
  options. New addresses are opened, before anything changes, and the others
  closed. If a new address cannot be opened, the reload fails.
- Environment variables keep the values they had at startup.
