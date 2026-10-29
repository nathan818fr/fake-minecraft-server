# Changelog

All notable changes to this project are documented in this file. The format
is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

A rewrite in Go: a single static binary, released as a much smaller container
image, that answers every client from Beta 1.8 to the latest snapshots. The
configuration is new: see [Upgrading from v1](docs/upgrading-from-v1.md).

### Added

- **Configuration file**, in YAML, with an environment variable for every
  setting.
- **Servers:** several statuses and kick messages in one instance, with any
  field of the status JSON, and text components.
- **Upstreams:** real servers polled for their status, shown while they
  answer.
- **Rules:** which server each client sees, by the host it connected to, its
  version, its network, or whether it is modded.
- **Scripting:** statuses and kick messages computed for each client, in
  Starlark, checked at startup and bounded at runtime.
- **Legacy clients**, Beta 1.8 to 1.6, answered in the format of each
  version.
- **Listeners:** several addresses, Unix sockets, and the PROXY protocol
  version 2.
- **Abuse resistance:** limits on connections, in total and per address,
  timeouts, and rate-limited logs that clients cannot forge.
- **Logs:** one line per connection, in text or JSON.
- **Reload** on `SIGHUP` and **graceful shutdown** on `SIGTERM`.
- **Image tags** for each major version (`v2`).
- **Commands:** `check` for the configuration, `healthcheck` for containers,
  and `version`.

### Changed

- The image, the defaults, the timeouts, the logs and how pings and
  configuration mistakes are handled: see
  [Upgrading from v1](docs/upgrading-from-v1.md).

### Deprecated

- The v1 variables (`MOTD`, `KICK_MESSAGE`, `LISTEN_PORT`...), replaced by
  the configuration file and its variables: read only without them, with a
  warning that gives their replacement.

### Removed

- `LISTEN_BACKLOG`, which has no replacement.
- `.env` files are no longer read: set the variables in the environment
  instead.

## [1.1.0] and earlier

The Node.js version. See the
[v1 README](https://github.com/nathan818fr/fake-minecraft-server/tree/v1.1.0#readme).

[Unreleased]: https://github.com/nathan818fr/fake-minecraft-server/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/nathan818fr/fake-minecraft-server/tree/v1.1.0
