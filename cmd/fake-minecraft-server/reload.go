package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/config"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
)

// reloader loads the configuration again on each reload signal (SIGHUP), and
// applies what can change while the server serves: everything the server
// reloads (see server.Server.Reload), with the listeners, and the log level.
// The log format needs a restart: the loggers made from the handler cannot
// change it, and log collectors parse one format.
type reloader struct {
	srv    *server.Server
	logger *slog.Logger
	level  *slog.LevelVar
	format logging.Format
	// listeners are the listeners served, with their settings.
	listeners []openListener
}

type openListener struct {
	settings config.Listener
	server.Listener
}

// listenAddr identifies a listener across reloads: its address as written,
// whatever its options.
type listenAddr struct{ network, address string }

func addrOf(l config.Listener) listenAddr {
	return listenAddr{l.Network, l.Address}
}

// run reloads on each signal of signals until ctx is canceled.
func (r *reloader) run(ctx context.Context, signals <-chan os.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			r.reload(ctx)
		}
	}
}

// reload loads the configuration and applies it, or logs its errors and keeps
// the running one.
func (r *reloader) reload(ctx context.Context) {
	r.logger.Info("reloading")
	cfg, err := config.Load(os.Environ())
	if err != nil {
		logScriptPrints(r.logger, slog.LevelError, cfg.ScriptPrints)
		r.fail(err)
		return
	}

	listeners, added, err := r.listen(ctx, cfg.Listeners)
	if err != nil {
		r.fail(err)
		return
	}
	served := make([]server.Listener, len(listeners))
	for i, l := range listeners {
		served[i] = l.Listener
	}
	if err := r.srv.Reload(cfg.Server, served...); err != nil {
		closeAll(added)
		r.fail(err)
		return
	}
	r.listeners = listeners
	r.level.Set(cfg.LogLevel)
	logScriptPrints(r.logger, slog.LevelDebug, cfg.ScriptPrints)
	if cfg.LogFormat != r.format {
		r.logger.Warn("log.format changed, restart to apply it")
	}
	r.logger.Info("reloaded")
}

func (r *reloader) fail(err error) {
	for _, err := range splitErrors(err) {
		r.logger.Error("reload failed", logging.Error(err))
	}
}

// listen returns the listeners of addrs: the open ones with the same address,
// with their new options, and new ones for the others, which it also returns
// in added. If one of them fails, it closes the new ones. The server closes
// the open listeners left out.
func (r *reloader) listen(ctx context.Context, addrs []config.Listener) (listeners []openListener, added []server.Listener, err error) {
	open := make(map[listenAddr]openListener, len(r.listeners))
	for _, l := range r.listeners {
		open[addrOf(l.settings)] = l
	}
	var missing []config.Listener
	for _, addr := range addrs {
		l, ok := open[addrOf(addr)]
		if !ok {
			missing = append(missing, addr)
			continue
		}
		delete(open, addrOf(addr))
		l.settings, l.Options = addr, addr.Options
		listeners = append(listeners, l)
	}

	added, err = listen(ctx, missing)
	if err != nil {
		// Such as 0.0.0.0:25565 in place of [::]:25565, which the old
		// listener holds until the server closes it.
		if len(open) > 0 && errors.Is(err, syscall.EADDRINUSE) {
			err = fmt.Errorf("%w (a listener that the reload removes may hold the address: restart to replace it)", err)
		}
		return nil, nil, err
	}
	for i, ln := range added {
		listeners = append(listeners, openListener{settings: missing[i], Listener: ln})
	}
	return listeners, added, nil
}
