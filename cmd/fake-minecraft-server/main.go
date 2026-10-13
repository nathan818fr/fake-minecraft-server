// Command fake-minecraft-server is a Minecraft server that only shows up in
// the server list and kicks every player trying to join.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/config"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
)

// version is set by the build of the image (Containerfile), with -ldflags
// "-X main.version=...". Other builds take it from their build info.
var version string

const usage = `Usage: fake-minecraft-server [command]

Without a command, runs the server, configured by FMS_CONFIG_FILE and the
FMS_ variables: see https://github.com/nathan818fr/fake-minecraft-server.

Commands:
  check                  Check the configuration, without starting the server
  healthcheck [address]  Check that the server answers at address, host:port
                         or unix:path (127.0.0.1:25565 by default), for the
                         health checks of containers
  version                Print the version
  help                   Print this help
`

func main() {
	// Until load sets up the configured logger, errors are logged in the
	// default format.
	slog.SetDefault(slog.New(logging.NewTextHandler(os.Stdout, nil)))
	var err error
	switch args := os.Args[1:]; {
	case len(args) == 0:
		err = run()
	case args[0] == "check" && len(args) == 1:
		err = check()
	case args[0] == "healthcheck" && len(args) <= 2:
		addr := defaultHealthcheckAddr
		if len(args) == 2 {
			addr = args[1]
		}
		err = healthcheck(addr)
	case args[0] == "version" && len(args) == 1:
		fmt.Println(currentVersion())
	case args[0] == "help" && len(args) == 1:
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		for _, err := range splitErrors(err) {
			slog.Error(err.Error())
		}
		os.Exit(1)
	}
}

func run() error {
	cfg, err := load()
	if err != nil {
		return err
	}

	logger := slog.Default()
	logger.Info("starting", "version", currentVersion())

	srv, err := server.New(cfg.Server, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal, restore the default behavior, so that a second
	// one kills the server without waiting for its connections.
	context.AfterFunc(ctx, stop)

	listeners, err := listen(ctx, cfg.Listeners)
	if err != nil {
		return err
	}
	return srv.Serve(ctx, listeners...)
}

// check loads the configuration and makes the server as run does, without
// listening.
func check() error {
	cfg, err := load()
	if err != nil {
		return err
	}
	if _, err := server.New(cfg.Server, slog.Default()); err != nil {
		return err
	}
	slog.Info("configuration valid")
	return nil
}

// load reads the configuration, and sets up the logger that it configures.
func load() (config.Config, error) {
	cfg, err := config.Load(os.Environ())
	if cfg.LogFormat != "" {
		// Errors returned from now on, including the ones of the other
		// settings, are logged in the configured format too.
		slog.SetDefault(slog.New(logging.NewHandler(os.Stdout, cfg.LogFormat, cfg.LogLevel)))
	}
	return cfg, err
}

// currentVersion returns the version set at build time or, failing that, the
// one in the build info of the binary, "(devel)" if unknown.
func currentVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "(devel)"
}

// splitErrors returns the errors that errors.Join joined in err, such as the
// configuration errors, so that each one is logged on its own line.
func splitErrors(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}
