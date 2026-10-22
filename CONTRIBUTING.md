# Contributing

Thanks for your interest in Fake Minecraft Server! Bug reports, ideas and pull
requests are welcome. For a large change, please open an issue first, so that
we can agree on the approach; the [goals](GOALS.md) of the project say what
fits in it.

## Development setup

The tools and tasks are managed by [mise](https://mise.jdx.dev), which
installs the versions locked in `.config/mise.lock`:

```shell
mise install          # Install Go, golangci-lint and dprint
mise run dev          # Run the server from source
mise run check        # Lint and test, as CI does
mise tasks            # List every task
```

Editors running golangci-lint need its configuration,
`--config .config/golangci.yaml`.

## Guidelines

- **Strict and bounded.** Accept only the exact packets expected, within
  vanilla limits, and bound what any client can make the server do: time,
  memory, reads and log lines.
- **Tests that can fail.** Each test checks a behavior that could break. Run
  `mise run check` before pushing.
- **Documentation with the code.** A change of behavior updates
  [the documentation](docs) and, if it changes a setting, the
  [examples](examples) and the [changelog](CHANGELOG.md).
- **No manually maintained data**, such as tables of Minecraft versions, so
  that new Minecraft releases need no change.
- **Comments explain why**: a vanilla limit, a client quirk, a security
  choice. They never restate the code.
- **Commits** have short, imperative subjects, such as "Add upstream servers".

## Screenshots

`mise run screenshots` takes the screenshots of the documentation in
Minecraft, from the [examples](examples) (Linux only).
