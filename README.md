# Fake Minecraft Server

This is a fake Minecraft server that only responds to server list requests and
sends a kick message when a player try to join.

![server list example](./.readme/server-list.png?raw=true)
![kick message example](./.readme/kick.png?raw=true)

## Usage

To start this server, simply run:

```shell
npm ci
npm run start
```

## Configuration

The configuration is defined via environment variables:

| Variable           | Description                                                                                                 | Default           |
| ------------------ | ----------------------------------------------------------------------------------------------------------- | ----------------- |
| `LISTEN_HOST`      | The hostname or IP address to listen on                                                                     | `::`              |
| `LISTEN_PORT`      | The port number to listen on                                                                                | `25565`           |
| `LISTEN_BACKLOG`   | The maximum number of queued pending connections                                                            | -                 |
| `KICK_MESSAGE`     | The message to send when a player try to join                                                               | `§cNot available` |
| `MOTD`             | The message displayed in the server list                                                                    | `§eHello World!`  |
| `FAVICON`          | The favicon displayed in the server list (path to a PNG file, or a string like `data:image/png;base64,XXX`) | -                 |
| `MAX_PLAYERS`      | The number of slots displayed in the server list                                                            | `0`               |
| `ONLINE_PLAYERS`   | The number of online players displayed in the server list                                                   | `0`               |
| `PROTOCOL_NAME`    | The protocol name reported in the server list                                                               | (empty)           |
| `PROTOCOL_VERSION` | The protocol version reported in the server list                                                            | `0`               |

To set these environment variables, you can either export them in your shell or
create a `.env` file in the root of the project with the following format:

```
LISTEN_PORT=25565
KICK_MESSAGE="§c§lSorry.\n§cCome back later."
MOTD="§eWelcome to My Server!\n§dJoin now!"
PROTOCOL_NAME="§cHey!"
```
