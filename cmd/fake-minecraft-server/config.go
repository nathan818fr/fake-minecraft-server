package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
)

// Environment variables read by loadConfig.
const (
	envListenAddr               = "LISTEN_ADDR"
	envKickMessage              = "KICK_MESSAGE"
	envMOTD                     = "MOTD"
	envFavicon                  = "FAVICON"
	envMaxPlayers               = "MAX_PLAYERS"
	envOnlinePlayers            = "ONLINE_PLAYERS"
	envProtocolName             = "PROTOCOL_NAME"
	envProtocolVersion          = "PROTOCOL_VERSION"
	envConnectionTimeout        = "CONNECTION_TIMEOUT"
	envHandshakeTimeout         = "HANDSHAKE_TIMEOUT"
	envMaxConnections           = "MAX_CONNECTIONS"
	envMaxConnectionsPerIP      = "MAX_CONNECTIONS_PER_IP"
	envConnectionRateLimitPerIP = "CONNECTION_RATE_LIMIT_PER_IP"
	envLogFormat                = "LOG_FORMAT"
	envLogLevel                 = "LOG_LEVEL"
	envLogRateLimit             = "LOG_RATE_LIMIT"
	envProxyProtocol            = "PROXY_PROTOCOL"
	envProxyProtocolTrusted     = "PROXY_PROTOCOL_TRUSTED"

	// Deprecated: replaced by LISTEN_ADDR.
	envListenHost = "LISTEN_HOST"
	envListenPort = "LISTEN_PORT"
)

const (
	defaultPort                = 25565
	defaultKickMessage         = "§cNot available"
	defaultMOTD                = "§eHello World!"
	defaultConnectionTimeout   = 3 * time.Second
	defaultHandshakeTimeout    = time.Second
	defaultMaxConnections      = 10000
	defaultMaxConnectionsPerIP = 8
	defaultRateLimitPerIP      = 10
	defaultLogRateLimit        = 1000
	defaultLogFormat           = logging.FormatText
	defaultLogLevel            = slog.LevelInfo
)

// clientProtocolVersion makes the server report the protocol version of each
// client.
const clientProtocolVersion = "client"

// ipv4MappedPrefixBits is the length of the prefix of IPv4-mapped IPv6
// addresses (::ffff:0:0/96), which hold the IPv4 address in their last 32 bits.
const ipv4MappedPrefixBits = 96

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// unixAddrPrefix starts the listen addresses of Unix sockets.
const unixAddrPrefix = "unix:"

type config struct {
	listenAddrs []listenAddr
	// listenerOptions are the options of every listener.
	listenerOptions server.ListenerOptions
	logFormat       logging.Format
	logLevel        slog.Level
	server          server.Config
	// warnings, such as uses of deprecated variables, are logged once the
	// logger is configured.
	warnings []string
}

// listenAddr is an address to listen on, on the "tcp" or "unix" network.
type listenAddr struct {
	network string
	address string
}

// String returns the address as written in LISTEN_ADDR.
func (a listenAddr) String() string {
	if a.network == "unix" {
		return unixAddrPrefix + a.address
	}
	return a.address
}

// loadConfig reads the configuration from the environment variables returned
// by lookup.
func loadConfig(lookup func(string) (string, bool)) (config, error) {
	env := &envReader{lookup: lookup}
	protocolVersion, mirrorProtocolVersion := env.getProtocolVersion(envProtocolVersion, 0)
	connectionTimeout := env.getDuration(envConnectionTimeout, defaultConnectionTimeout)
	// By default, at most CONNECTION_TIMEOUT, so that a short
	// CONNECTION_TIMEOUT works without also setting HANDSHAKE_TIMEOUT.
	handshakeTimeout := env.getDuration(envHandshakeTimeout, min(defaultHandshakeTimeout, connectionTimeout))

	cfg := config{
		listenAddrs: env.getListenAddrs(),
		listenerOptions: server.ListenerOptions{
			ProxyProtocol:  env.getBool(envProxyProtocol, false),
			TrustedProxies: env.getPrefixes(envProxyProtocolTrusted),
		},
		logFormat: env.getLogFormat(envLogFormat, defaultLogFormat),
		logLevel:  env.getLogLevel(envLogLevel, defaultLogLevel),
		server: server.Config{
			KickMessage:              textComponent(env.get(envKickMessage, defaultKickMessage)),
			MOTD:                     textComponent(env.get(envMOTD, defaultMOTD)),
			Favicon:                  env.getFavicon(envFavicon),
			MaxPlayers:               env.getInt32(envMaxPlayers, 0),
			OnlinePlayers:            env.getInt32(envOnlinePlayers, 0),
			VersionName:              env.get(envProtocolName, ""),
			ProtocolVersion:          protocolVersion,
			MirrorProtocolVersion:    mirrorProtocolVersion,
			ConnectionTimeout:        connectionTimeout,
			HandshakeTimeout:         handshakeTimeout,
			MaxConnections:           env.getInt(envMaxConnections, defaultMaxConnections, 0, math.MaxInt32),
			MaxConnectionsPerIP:      env.getInt(envMaxConnectionsPerIP, defaultMaxConnectionsPerIP, 0, math.MaxInt32),
			ConnectionRateLimitPerIP: env.getInt(envConnectionRateLimitPerIP, defaultRateLimitPerIP, 0, math.MaxInt32),
			LogRateLimit:             env.getInt(envLogRateLimit, defaultLogRateLimit, 0, math.MaxInt32),
		},
	}
	if cfg.server.HandshakeTimeout > cfg.server.ConnectionTimeout {
		env.fail(envHandshakeTimeout, fmt.Errorf("must not exceed %s", envConnectionTimeout))
	}
	if len(cfg.listenerOptions.TrustedProxies) > 0 && !cfg.listenerOptions.ProxyProtocol {
		env.fail(envProxyProtocolTrusted, fmt.Errorf("requires %s=true", envProxyProtocol))
	}
	// Trusting any TCP peer must be explicit: anyone reaching the server could
	// then send a header with a forged address.
	tcp := slices.ContainsFunc(cfg.listenAddrs, func(a listenAddr) bool { return a.network == "tcp" })
	if cfg.listenerOptions.ProxyProtocol && tcp && env.get(envProxyProtocolTrusted, "") == "" {
		env.fail(envProxyProtocolTrusted, errors.New("must list the proxies allowed to send headers, such as 10.0.0.0/8, or 0.0.0.0/0,::/0 for any address"))
	}
	cfg.warnings = env.warnings
	return cfg, errors.Join(env.errs...)
}

// envReader reads typed variables and collects every error, so that all
// configuration mistakes are reported at once.
type envReader struct {
	lookup   func(string) (string, bool)
	errs     []error
	warnings []string
}

func (e *envReader) fail(key string, err error) {
	e.errs = append(e.errs, fmt.Errorf("%s: %w", key, err))
}

func (e *envReader) warn(msg string) {
	e.warnings = append(e.warnings, msg)
}

// get returns the value of key, or fallback if it is unset or empty.
func (e *envReader) get(key, fallback string) string {
	if value, ok := e.lookup(key); ok && value != "" {
		return value
	}
	return fallback
}

func (e *envReader) getInt(key string, fallback, lo, hi int) int {
	value := e.get(key, "")
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < lo || n > hi {
		e.fail(key, fmt.Errorf("must be an integer between %d and %d, got %q", lo, hi, value))
		return fallback
	}
	return n
}

func (e *envReader) getInt32(key string, fallback int32) int32 {
	return int32(e.getInt(key, int(fallback), math.MinInt32, math.MaxInt32))
}

func (e *envReader) getDuration(key string, fallback time.Duration) time.Duration {
	value := e.get(key, "")
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		e.fail(key, fmt.Errorf("must be a positive duration such as \"3s\" or \"500ms\", got %q", value))
		return fallback
	}
	return d
}

func (e *envReader) getBool(key string, fallback bool) bool {
	value := e.get(key, "")
	if value == "" {
		return fallback
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		e.fail(key, fmt.Errorf("must be true or false, got %q", value))
		return fallback
	}
	return b
}

// getProtocolVersion returns the protocol version to report, or whether to
// mirror the client's one ("client").
func (e *envReader) getProtocolVersion(key string, fallback int32) (version int32, mirror bool) {
	value := e.get(key, "")
	switch value {
	case "":
		return fallback, false
	case clientProtocolVersion:
		return 0, true
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		e.fail(key, fmt.Errorf("must be an integer or %q, got %q", clientProtocolVersion, value))
		return fallback, false
	}
	return int32(n), false
}

// getListenAddrs parses LISTEN_ADDR, a comma-separated list of host:port and
// unix:path addresses. It falls back to the deprecated LISTEN_HOST and
// LISTEN_PORT if LISTEN_ADDR is unset.
func (e *envReader) getListenAddrs() []listenAddr {
	value := e.get(envListenAddr, "")
	if e.get(envListenHost, "") != "" || e.get(envListenPort, "") != "" {
		if value != "" {
			e.fail(envListenAddr, fmt.Errorf("cannot be combined with the deprecated %s and %s", envListenHost, envListenPort))
			return nil
		}
		addr := listenAddr{"tcp", net.JoinHostPort(
			e.get(envListenHost, ""),
			strconv.Itoa(e.getInt(envListenPort, defaultPort, 1, math.MaxUint16)),
		)}
		e.warn(fmt.Sprintf("%s and %s are deprecated, use %s=%s instead", envListenHost, envListenPort, envListenAddr, addr))
		return []listenAddr{addr}
	}
	if value == "" {
		return []listenAddr{{"tcp", net.JoinHostPort("", strconv.Itoa(defaultPort))}}
	}

	var addrs []listenAddr
	listed := false
	for item := range strings.SplitSeq(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		listed = true
		addr, err := parseListenAddr(item)
		if err != nil {
			e.fail(envListenAddr, err)
			continue
		}
		// A second one would fail when listening, and for a Unix socket, with
		// an error telling that another server uses it.
		if slices.Contains(addrs, addr) {
			e.fail(envListenAddr, fmt.Errorf("lists %q twice", item))
			continue
		}
		addrs = append(addrs, addr)
	}
	if !listed {
		e.fail(envListenAddr, fmt.Errorf("must list at least one address, got %q", value))
	}
	return addrs
}

// parseListenAddr checks the syntax of a host:port or unix:path address. What
// depends on the system, such as whether the address is free, is checked when
// listening.
func parseListenAddr(s string) (listenAddr, error) {
	if path, ok := strings.CutPrefix(s, unixAddrPrefix); ok {
		if path == "" {
			return listenAddr{}, fmt.Errorf("%q has no socket path", s)
		}
		return listenAddr{"unix", path}, nil
	}
	_, port, err := net.SplitHostPort(s)
	if err != nil {
		return listenAddr{}, fmt.Errorf("%q must be host:port or %spath", s, unixAddrPrefix)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > math.MaxUint16 {
		return listenAddr{}, fmt.Errorf("%q has an invalid port, must be between 1 and %d", s, math.MaxUint16)
	}
	return listenAddr{"tcp", s}, nil
}

func (e *envReader) getLogFormat(key string, fallback logging.Format) logging.Format {
	value := e.get(key, "")
	if value == "" {
		return fallback
	}
	format, err := logging.ParseFormat(value)
	if err != nil {
		e.fail(key, err)
		return fallback
	}
	return format
}

func (e *envReader) getLogLevel(key string, fallback slog.Level) slog.Level {
	value := e.get(key, "")
	if value == "" {
		return fallback
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		e.fail(key, fmt.Errorf("must be debug, info, warn or error, got %q", value))
		return fallback
	}
	return level
}

// getFavicon returns the value of key as is if it is already a data URI, and
// otherwise reads the PNG file it points to.
func (e *envReader) getFavicon(key string) string {
	value := e.get(key, "")
	if value == "" || strings.HasPrefix(value, "data:") {
		return value
	}
	data, err := os.ReadFile(value)
	if err != nil {
		e.fail(key, err)
		return ""
	}
	if !bytes.HasPrefix(data, pngSignature) {
		e.fail(key, fmt.Errorf("%s is not a PNG file", value))
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

// getPrefixes parses a comma-separated list of IP addresses and CIDR prefixes.
func (e *envReader) getPrefixes(key string) []netip.Prefix {
	var prefixes []netip.Prefix
	for item := range strings.SplitSeq(e.get(key, ""), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		prefix, err := parsePrefix(item)
		if err != nil {
			e.fail(key, err)
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

// parsePrefix parses an IP address, as a prefix of its full length, or a CIDR
// prefix.
func parsePrefix(s string) (netip.Prefix, error) {
	var prefix netip.Prefix
	var err error
	if strings.Contains(s, "/") {
		prefix, err = netip.ParsePrefix(s)
	} else {
		var addr netip.Addr
		addr, err = netip.ParseAddr(s)
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address or CIDR prefix", s)
	}
	// Client addresses are unmapped, so IPv4-mapped prefixes must be too.
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < ipv4MappedPrefixBits {
			return netip.Prefix{}, fmt.Errorf("%q is an IPv4-mapped prefix shorter than /%d", s, ipv4MappedPrefixBits)
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-ipv4MappedPrefixBits)
	}
	return prefix.Masked(), nil
}

// textComponent returns s as is if it is a JSON object, and wraps it in a
// plain text component otherwise.
func textComponent(s string) json.RawMessage {
	if strings.HasPrefix(s, "{") && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	component, _ := json.Marshal(map[string]string{"text": s})
	return component
}
