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

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
)

// version is set by the build of the image (Containerfile), with -ldflags
// "-X main.version=...". Other builds take it from their build info.
var version string

const usage = `Usage: fake-minecraft-server [command]

Without a command, runs the server, configured by environment variables:
see https://github.com/nathan818fr/fake-minecraft-server.

Commands:
  version                Print the version
  help                   Print this help
`

func main() {
	// Until run sets up the configured logger, errors are logged in the
	// default format.
	slog.SetDefault(slog.New(logging.NewTextHandler(os.Stdout, nil)))
	var err error
	switch args := os.Args[1:]; {
	case len(args) == 0:
		err = run()
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
	cfg, err := loadConfig(os.LookupEnv)
	if err != nil {
		return err
	}

	logger := slog.New(logging.NewHandler(os.Stdout, cfg.logFormat, cfg.logLevel))
	// Errors returned from now on are logged in the configured format too.
	slog.SetDefault(logger)
	logger.Info("starting", "version", currentVersion())
	for _, warning := range cfg.warnings {
		logger.Warn(warning)
	}

	srv, err := server.New(cfg.server, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal, restore the default behavior, so that a second
	// one kills the server without waiting for its connections.
	context.AfterFunc(ctx, stop)

	listeners, err := listen(ctx, cfg.listenAddrs, cfg.listenerOptions)
	if err != nil {
		return err
	}
	return srv.Serve(ctx, listeners...)
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
