# Logs

The server logs to the standard output: one line per connection, once it
ends, and the events of the server and of its upstreams. Logs have no
stability guarantee: their messages and fields may change in any release.

## Format

`log.format: text`, the default, is for people:

```text
[2026-10-22T08:15:02.517Z] INFO  starting: version=v2.0.0
[2026-10-22T08:15:02.518Z] INFO  listening: listener=[::]:25565
[2026-10-22T08:15:13.517Z] INFO  [198.51.100.7]:45610 server list ping: protocol=773 host="mc.example.com" port=25565 rule=default server=default source=static ping_request=true duration=109.2ms
[2026-10-22T08:15:17.083Z] INFO  [198.51.100.7]:51664 login attempt: protocol=773 host="mc.example.com" port=25565 rule=default server=default username="Notch" uuid=069a79f4-44e9-4726-a5be-fca90e38aaf5 duration=400µs
[2026-10-22T08:15:26.418Z] INFO  [203.0.113.9]:46928 invalid connection: data="GET / HTTP/1.1\r\nHost: 192.0.2.1:25565\r\nUser-Agent: Mozilla/5.0\r\n" duration=100µs error="reading handshake: unexpected packet: id 0x45, want 0x0"
```

`log.format: json` writes one JSON object per line, for log collectors, with
the same fields, except that the address of the client is under `remote`, and
durations are numbers of milliseconds (`duration_ms`):

```json
{"time":"2026-10-22T08:15:13.517Z","level":"INFO","msg":"server list ping","remote":"198.51.100.7:45610","protocol":773,"host":"mc.example.com","port":25565,"rule":"default","server":"default","source":"static","ping_request":true,"duration_ms":109.2}
```

## Levels and rate limit

`log.level` sets the minimum level logged: `debug`, `info` (the default),
`warn` or `error`. `debug` adds every poll of the upstreams and what scripts
print; `warn` leaves out the line of each connection, but keeps rejected
connections, failed scripts and unavailable upstreams.

`log.rate_limit` bounds the lines logged per second about connections (1,000
by default, `0` for no limit), so that floods cannot fill the disk. The next
line logged counts the ones dropped (`dropped=`).

## Connection lines

Each connection logs one line once it ends, at the `info` level:

| Message                              | Logged when                                                                                                                                                    |
| ------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `server list ping`                   | A client asked for the status shown in its server list                                                                                                         |
| `login attempt`                      | A player tried to join, and was kicked                                                                                                                         |
| `transfer attempt`                   | A player transferred from another server (1.20.5+) tried to join, and was kicked                                                                               |
| `legacy server list ping`            | A client older than 1.7 asked for the status                                                                                                                   |
| `legacy login attempt`               | A player with a client older than 1.7 tried to join, and was kicked                                                                                            |
| `invalid connection`                 | Something other than a Minecraft client connected, such as an HTTP request or a TLS handshake, or a client sent nothing within `connections.handshake_timeout` |
| `connection closed before handshake` | A client left before sending its handshake, such as port scanners and health checks                                                                            |

Connections over a limit are reset, and logged at the `warn` level as
`connection rejected`, with their addresses and the `limit` they exceeded:
`connections.max`, `connections.max_per_ip` or
`connections.rate_limit_per_ip`.

The fields come in this order, and the ones that do not apply, or that the
client did not send, are left out:

| Field                 | Value                                                                                                                                                                                                        |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `remote`              | The address of the client, the one that the proxy sent behind the [PROXY protocol](deployment.md#behind-a-proxy), or `unix` on a Unix socket. The text format writes it before the message, without its name |
| `proxy`               | The address of the proxy, behind the PROXY protocol                                                                                                                                                          |
| `listener`            | The address that the client connected to, when the server listens on several                                                                                                                                 |
| `legacy_ping_version` | The format of a legacy ping: `beta-1.3`, `1.4-1.5` or `1.6`                                                                                                                                                  |
| `protocol`            | The protocol version of the client, `-1` if it sent none (legacy logins before 1.3, legacy pings before 1.6)                                                                                                 |
| `host`, `port`        | The host and port that the client typed to connect                                                                                                                                                           |
| `host_extra`          | What modded clients and proxies add after the host, such as `FML3\x00` for Forge                                                                                                                             |
| `rule`                | The [rule](rules.md) that answered: its name, its index from 0, or `default` for the implicit last rule                                                                                                      |
| `server`              | The ID of the server of that rule                                                                                                                                                                            |
| `source`              | Where the answer came from: `script` if a hook answered, or else `upstream` if the upstream of the server did, or else `static`. Joins only have it when a `join_script` answered                            |
| `failed_rules`        | The rules whose condition failed for the client, which did not apply (see [Checks and failures](scripting.md#checks-and-failures))                                                                           |
| `ping_request`        | Whether the client measured its latency after getting the status, as Minecraft clients do                                                                                                                    |
| `username`, `uuid`    | What the player sent to join, `uuid` from 1.19.3                                                                                                                                                             |
| `data`                | The first 64 bytes that an invalid connection sent, to help identify scanners and other bots                                                                                                                 |
| `limit`               | The setting of the limit that a rejected connection exceeded                                                                                                                                                 |
| `duration`            | How long the connection lasted                                                                                                                                                                               |
| `dropped`             | The lines that `log.rate_limit` left out before this one                                                                                                                                                     |
| `error`               | Why the connection ended, if not normally                                                                                                                                                                    |

## Escaping

Values that clients and servers send, and errors, are escaped so that they
cannot forge lines, invisible and bidirectional characters included, such as
`\u202e`. For example, a username with a line break is logged as
`username="Notch\nINFO  fake"` in text, and `"username":"Notch\nINFO  fake"`
in JSON, which decodes to the username sent.
