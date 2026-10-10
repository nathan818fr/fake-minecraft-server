# Project goals

Fake Minecraft Server answers server list pings with a status, and kicks
players who try to join, or transfers them to another server. Rules choose
what each client gets, and statuses can come live from a real server. It
keeps closed servers, or servers under maintenance, answering with a message.

## Goals

In order of priority:

1. **Strict protocol matching.** Accept only the exact packets expected at each
   step, within vanilla limits (or tighter when safe, such as byte limits for
   ASCII strings). Support every client from Beta 1.8 to the latest snapshots.
2. **Abuse resistance.** Bound everything a client can make the server do:
   connection lifetime, handshake time, concurrent connections (global and per
   IP), bytes read, log lines. Reject invalid traffic after its first bytes.
   Never let untrusted data forge logs.
3. **Low resource usage.** A connection costs a few KiB of memory, so that a
   small instance holds thousands of them.
4. **Useful logs.** One line per connection, with the values that clients
   sent, never normalized, and enough detail to study scanners and bad actors.
5. **Strict configuration.** Invalid settings stop the server at startup, and
   leave the running configuration in place on a reload, with every mistake
   reported at once, named after its setting.

## Non-goals

- **Hosting players.** Players never play on this server: it runs no world,
  and never proxies their connection to a real server.
- **Authenticating players.** Connections are never encrypted and accounts
  never checked, so the usernames in logs are what clients claim.
- **Stopping network floods.** The server bounds what each connection costs,
  but a flood of new connections must be rate limited by a firewall.
- **Bedrock Edition.** Only Java Edition clients are answered.
