# Deployment

[Getting started](getting-started.md) runs the container image. This page
covers the rest: running the binary instead, exposing the server, and sizing
and protecting it.

- [Without a container](#without-a-container)
- [Listeners](#listeners)
- [Unix sockets](#unix-sockets)
- [Behind a proxy](#behind-a-proxy)
- [Rate limiting new connections](#rate-limiting-new-connections)
- [Resources](#resources)
- [Hardening](#hardening)
- [Verifying a release](#verifying-a-release)

## Without a container

Each [release](https://github.com/nathan818fr/fake-minecraft-server/releases)
has binaries for Linux and macOS, on amd64 and arm64, with their checksums.
Otherwise, build the binary from source.

### Build from source

Building requires [Go](https://go.dev/dl/) 1.26 or later:

```shell
git clone https://github.com/nathan818fr/fake-minecraft-server.git
cd fake-minecraft-server
CGO_ENABLED=0 go build -trimpath -o fake-minecraft-server ./cmd/fake-minecraft-server
FMS_CONFIG_FILE=examples/maintenance.yaml ./fake-minecraft-server
```

The result is a static binary, with no runtime dependency, which can be copied
to any Linux machine of the same architecture (`GOOS` and `GOARCH` select
another one).

### Run as a systemd service

With the binary in `/usr/local/bin` and the configuration in
`/etc/fake-minecraft-server/`:

```ini
# /etc/systemd/system/fake-minecraft-server.service
[Unit]
Description=Fake Minecraft Server
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/fake-minecraft-server
ExecReload=/bin/kill -HUP $MAINPID
Environment=FMS_CONFIG_FILE=/etc/fake-minecraft-server/config.yaml
Restart=on-failure
DynamicUser=yes
# Only to listen on a port below 1024:
# AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

```shell
sudo systemctl daemon-reload
sudo systemctl enable --now fake-minecraft-server
journalctl -u fake-minecraft-server -f
```

`sudo systemctl reload fake-minecraft-server` sends `SIGHUP`, which
[reloads](configuration.md#reloading) the configuration.
`systemd-analyze security fake-minecraft-server` lists the sandboxing options
that can be added to the unit, such as `ProtectSystem=strict`.

## Listeners

`listen` takes one or more addresses, each one alone, or with its options
(see [Configuration](configuration.md#listeners)):

```yaml
listen:
  - ":25565"                    # Every interface, IPv4 and IPv6
  - "192.0.2.10:25566"          # One address
  - "[2001:db8::10]:25566"      # IPv6, in brackets and quotes
  - "unix:/run/fake-minecraft-server/server.sock"
  - address: ":25567"
    proxy_protocol: true
    trusted_proxies: [10.0.0.0/8]
```

With the variable, addresses are comma-separated
(`FMS_LISTEN=:25565,unix:/run/fake-minecraft-server/server.sock`), or in YAML
flow style for options, with a space after each `:`:
`FMS_LISTEN='[{ address: ":25567", proxy_protocol: true, trusted_proxies: [10.0.0.0/8] }]'`.

The server starts on all its addresses or none, and its limits apply to all
of them together.

## Unix sockets

Unix sockets suit a proxy on the same machine:

- Their path is limited to 107 bytes on Linux. The socket file is created
  with the permissions that the umask allows, which decide who can connect,
  and removed when the server stops. A file left by a crash is replaced on
  the next start, but not one that a running server listens on: a lock file
  next to it keeps two servers from sharing a socket.
- `unix:@name` is an abstract socket (Linux), without a file: any local
  process can connect to it.
- Their clients have no address: they are logged as `unix`, and not limited
  per address, unless a PROXY protocol header tells their address.

With `proxy_protocol`, a Unix socket trusts whoever can connect to it: any
process that can connect can make up the addresses of its clients, bypass
the limits per address, forge the addresses of logs, and match the network
conditions of rules. Put the socket in a directory that only the server and
the proxy can enter, never in a shared directory such as `/tmp`, and let the
umask give it to their group, rather than changing its permissions after it
is created. With systemd:

```ini
[Service]
RuntimeDirectory=fake-minecraft-server
RuntimeDirectoryMode=0750
# The group of the proxy:
Group=haproxy
UMask=0007
Environment=FMS_LISTEN=unix:/run/fake-minecraft-server/proxy.sock
```

With containers, share a volume between the server and the proxy only.

## Behind a proxy

Behind a TCP proxy, every connection comes from the proxy. With the
[PROXY protocol](https://www.haproxy.org/download/3.0/doc/proxy-protocol.txt),
the proxy sends the address of each client first, so that logs, the limits
per address and the network conditions of rules see the client.
Only version 2, binary, is supported: version 1, text, is not.

Enable it on the listeners that the proxy connects to:

```yaml
listen:
  - ":25565"                         # Direct clients, if any
  - address: ":25566"
    proxy_protocol: true
    trusted_proxies: [10.0.0.0/8]    # The addresses of the proxies
  - address: unix:/run/fake-minecraft-server/proxy.sock
    proxy_protocol: true             # A proxy on the same machine
```

Then every connection to these listeners must start with a PROXY protocol
header, within `connections.handshake_timeout`, or it is reset. TCP listeners
must list their proxies in `trusted_proxies`: connections from other
addresses are reset, as anyone could otherwise send a header with a forged
address. To trust any address, list `0.0.0.0/0` and `::/0`, and make sure that
the server cannot be reached without the proxy.

Headers without a client address, such as the health checks of some proxies,
keep the address of the proxy.

With [HAProxy](https://www.haproxy.org), use `send-proxy-v2`:

```text
frontend minecraft
    mode tcp
    bind :25565
    default_backend fake_minecraft_server

backend fake_minecraft_server
    mode tcp
    server fms 127.0.0.1:25566 send-proxy-v2
```

The server is meant to face clients, or a TCP proxy, rather than to sit behind
a Minecraft proxy such as BungeeCord or Velocity: its player forwarding is
not supported, and the legacy forwarding of BungeeCord (`ip_forward`) makes
handshakes longer than the 255 characters that vanilla servers accept, so
they are rejected.

## Rate limiting new connections

The server limits the new connections of each address
(`connections.rate_limit_per_ip`), but still accepts each one, which costs
CPU under a flood. A firewall rejects them before. For example, with [nftables](https://wiki.nftables.org), at most
10 new connections per second for each IPv4 address and IPv6 /64, with bursts
of 20:

```text
table inet fake_minecraft_server {
	set flood_v4 {
		type ipv4_addr
		flags dynamic, timeout
		timeout 1m
	}
	set flood_v6 {
		type ipv6_addr
		flags dynamic, timeout
		timeout 1m
	}

	chain input {
		type filter hook input priority filter; policy accept;
		tcp dport 25565 ct state new add @flood_v4 { ip saddr limit rate over 10/second burst 20 packets } drop
		tcp dport 25565 ct state new add @flood_v6 { ip6 saddr and ffff:ffff:ffff:ffff:: limit rate over 10/second burst 20 packets } drop
	}
}
```

Behind a proxy, rate-limit on the proxy instead.

## Resources

Each open connection holds a few kilobytes of memory, so `connections.max`
(10,000 by default) bounds the memory of the server too.

Each connection takes a file descriptor: the limit on open files must exceed
`connections.max`. The server raises its soft limit to the hard one at
startup, so only the hard limit matters (`ulimit -Hn`, `LimitNOFILE=` with
systemd, `--ulimit nofile=` with containers).

In a container with a memory limit, setting the
[`GOMEMLIMIT`](https://pkg.go.dev/runtime#hdr-Environment_Variables)
variable a little below it, such as `GOMEMLIMIT=200MiB` for a 256 MiB limit,
makes the server collect garbage harder when it approaches the limit, rather
than being killed.

## Hardening

The server opens no outbound connection, except to poll the upstreams, and
the DNS queries that their addresses need.

## Verifying a release

GitHub attests that the image and the archives of each release were built by
its release workflow, from the tagged commit. The
[GitHub CLI](https://cli.github.com/) checks it:

```shell
gh attestation verify oci://ghcr.io/nathan818fr/fake-minecraft-server:v2 --repo nathan818fr/fake-minecraft-server
gh attestation verify fake-minecraft-server_2.0.0_linux_amd64.tar.gz --repo nathan818fr/fake-minecraft-server
```
