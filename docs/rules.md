# Rules

Rules tell which [server](configuration.md#servers) each client sees. Each
rule names a server, and may have conditions: on the host that the client
connected to (`host`), and any other one written in Starlark (`when`).

```yaml
rules:
  - name: creative
    host: [.creative.example.com]
    server: creative
  - name: outdated
    when: 'client.legacy or (client.protocol >= 0 and client.protocol < 47)'
    server: outdated
  - name: lobby
    host: [.example.com]
    server: lobby
```

- Rules are tried in order. The server of the first rule that applies
  answers, status requests and joins alike, and the next rules are never
  tried: a server never takes anything from another one.
- A rule applies to the clients that match all its conditions, and a rule
  without conditions to every client, so a rule after it would never apply:
  that is an error.
- After the rules, an implicit last rule names the `default` server, for the
  clients that no rule matches, such as scanners that connect to the IP
  address of the server.

Logs tell which rule answered each client (`rule=`, its name or its index,
from 0, or `default`) and its server (`server=`).

## Hosts

`host` lists host patterns, one of which must match the host that the client
connected to, the address typed in its server list:

| Pattern            | Matches                                                              |
| ------------------ | -------------------------------------------------------------------- |
| `play.example.com` | That host only                                                       |
| `"*.example.com"`  | The subdomains of `example.com`, at any depth, but not `example.com` |
| `.example.com`     | `example.com` and its subdomains, at any depth                       |

Hosts match in any case, with or without a trailing dot, whatever modded
clients and proxies add after them (`host_extra`, such as `FML3\x00` for
Forge). Clients that send no host, legacy pings before 1.6 and the oldest
legacy logins, match no host pattern.

## Conditions

`when` is a [Starlark](scripting.md) expression that returns `True` or
`False`, and sees the client in `client`, with the values it sent, as it sent
them:

```yaml
rules:
  - when: 'client.protocol >= 767'            # 1.21 and later
    server: recent
  - when: '"FML" in client.host_extra'        # Forge clients
    server: modded
  - when: 'fms.in_network(client.ip, "10.0.0.0/8", "fd00::/8")'
    server: internal
  - when: |
      fms.match_host(client.host, ".example.com")
        and client.request == "join"
    server: lobby
```

The most used fields are `client.host`, `client.protocol`, `client.legacy`,
`client.ip` and `client.request` (`"ping"` or `"join"`): see
[Scripting](scripting.md#client) for all of them, and for the functions that
conditions can call, such as `fms.in_network` and `fms.matches`.

Conditions can also see the state of the upstreams, with `ctx`: see
[Fallbacks](#fallbacks).

At startup, each condition runs with sample clients, and an error is a
configuration mistake. Later, an error with a real client skips the rule, as
if the condition were `False` (see
[Checks and failures](scripting.md#checks-and-failures)).

A few things to keep in mind:

- Unknown protocols are `-1`: status checkers and some legacy clients send no
  version, so `client.protocol < 47` matches them too.
- Usernames and UUIDs are what clients claim: the server checks no account.
  Recognize staff by their network, never by their name.

## Recipes

### One server per address

Show a different server for each address that players type, and the
`default` server to everything else, such as the bare IP address:

```yaml
servers:
  survival:
    status: { description: "§aSurvival" }
  creative:
    status: { description: "§bCreative" }
rules:
  - host: [survival.example.com, mc.example.com]
    server: survival
  - host: [creative.example.com]
    server: creative
```

### Outdated clients

Clients older than a version, with a message in place of the player counts:

```yaml
servers:
  outdated:
    client_protocol: never
    status:
      description: "§cExample Network requires Minecraft 1.8 or later"
      version: { name: "§c1.8+", protocol: 47 }
    kick: "§cPlease update to Minecraft 1.8 or later."
rules:
  - name: outdated
    when: 'client.legacy or (client.protocol >= 0 and client.protocol < 47)'
    server: outdated
```

`client_protocol: never` keeps the protocol version of the status, so that
outdated clients see `1.8+` in red.

### Staff previews

A server that only the office network sees, before it opens to everyone:

```yaml
script: |
  OFFICE = ["198.51.100.0/24", "2001:db8:100::/48"]

  def from_office(client):
      return fms.in_network(client.ip, *OFFICE)

servers:
  staging:
    status: { description: "§eStaging §7- staff only" }
rules:
  - host: [staging.example.com]
    when: from_office(client)
    server: staging
```

Behind a proxy, enable the [PROXY protocol](deployment.md#behind-a-proxy),
so that `client.ip` is the address of the client, not the proxy's.

### Fallbacks

Show a real server while it answers, a backup while only the backup answers,
and a maintenance screen while both are down:

```yaml
upstreams:
  lobby: { address: lobby.example.com }
  backup: { address: backup.example.com }
servers:
  maintenance:
    status:
      description: "§cMaintenance, back soon"
      version: { name: "§cMaintenance", protocol: -1 }
rules:
  - when: 'ctx.upstream("lobby").status != None'
    server: lobby
  - when: 'ctx.upstream("backup").status != None'
    server: backup
  - server: maintenance
```

### Splitting clients

`fms.bucket` splits clients evenly and steadily: the same address always
lands in the same bucket, across requests and restarts. For example, to test
two messages of the day:

```yaml
servers:
  motd_a:
    status: { description: "§6Example Network §7- survival, creative" }
  motd_b:
    status: { description: "§6Example Network §7- now with minigames!" }
rules:
  - when: 'fms.bucket("motd:" + client.ip, 2) == 0'
    server: motd_a
  - server: motd_b
```
