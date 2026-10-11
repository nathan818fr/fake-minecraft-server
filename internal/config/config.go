// Package config reads the configuration of the server from a YAML file and
// from environment variables, which override it, and checks it.
//
// The settings, their defaults and the rules they follow are in config.go,
// and the way they are read is in load.go.
package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/internal/condition"
	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/internal/server"
	"github.com/nathan818fr/fake-minecraft-server/internal/status"
)

const (
	defaultPort                = 25565
	defaultKickMessage         = "§cNot available"
	defaultConnectionTimeout   = 3 * time.Second
	defaultHandshakeTimeout    = time.Second
	defaultMaxConnections      = 10000
	defaultMaxConnectionsPerIP = 8
	defaultLogFormat           = logging.FormatText
	defaultLogLevel            = slog.LevelInfo
	defaultLogRateLimit        = 1000

	// defaultServerID is the ID of the server that the implicit last rule
	// targets, which always exists (see addDefaultServer).
	defaultServerID = "default"
	// defaultRuleName is the name of the implicit last rule in logs; the others
	// are named by their index.
	defaultRuleName = "default"
	// defaultStatus is the built-in status of the default server, used unless
	// the configuration file sets one (see addDefaultServer).
	defaultStatus = `{"description":{"text":"` + defaultKickMessage + `"},"players":{"max":0,"online":0,"sample":[]}}`
)

type Config struct {
	Listeners []Listener
	// LogFormat is empty if invalid, as Load also returns the configuration
	// with its mistakes.
	LogFormat logging.Format
	LogLevel  slog.Level
	Server    server.Config
}

const unixAddrPrefix = "unix:"

// Listener is an address to listen on, on the "tcp" or "unix" network, with
// the options of the connections that it accepts.
type Listener struct {
	Network string
	Address string
	Options server.ListenerOptions
}

// settings are the settings as written by users, in the configuration file
// and the environment variables (see Load). Unset settings are zero, and
// take their default when checked.
type settings struct {
	Listen []listenSettings `yaml:"listen"`

	Connections connectionSettings `yaml:"connections"`
	Log         logSettings        `yaml:"log"`
	// NoSoftwareSignature leaves the signature of this software out of the
	// statuses (see server.Config).
	NoSoftwareSignature bool `yaml:"no_software_signature"`

	Servers map[string]serverSettings `yaml:"servers"`
	Rules   []ruleSettings            `yaml:"rules"`
}

// listenSettings are the settings of a listener: its address, written alone,
// or in a mapping with its options (see UnmarshalYAML).
type listenSettings struct {
	Address string `yaml:"address"`
	// ProxyProtocol requires every connection to start with a PROXY protocol
	// header, from the TCP peers in TrustedProxies or from any Unix socket
	// peer.
	ProxyProtocol  bool     `yaml:"proxy_protocol"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

type connectionSettings struct {
	Timeout          optional[time.Duration] `yaml:"timeout"`
	HandshakeTimeout optional[time.Duration] `yaml:"handshake_timeout"`
	Max              optional[int]           `yaml:"max"`
	MaxPerIP         optional[int]           `yaml:"max_per_ip"`
}

type logSettings struct {
	Format    string        `yaml:"format"`
	Level     string        `yaml:"level"`
	RateLimit optional[int] `yaml:"rate_limit"`
}

// statusSettings are the fields of a status JSON. Its favicon may be the path
// of a PNG file. It is an alias, so that the YAML library decodes the
// mappings it holds as map[string]any too.
type statusSettings = map[string]any

type serverSettings struct {
	Status         statusSettings `yaml:"status"`
	ClientProtocol string         `yaml:"client_protocol"`
	// Kick is plain text, or a JSON text component written in YAML or in
	// JSON.
	Kick any `yaml:"kick"`
}

type ruleSettings struct {
	Name   string   `yaml:"name"`
	Host   []string `yaml:"host"`
	Server string   `yaml:"server"`
}

// addDefaultServer adds the default server to s, with the built-in status
// unless it has one, so that variables change it like a server of the
// configuration file, and so that the implicit last rule always applies.
func (s *settings) addDefaultServer() {
	server := s.Servers[defaultServerID]
	if server.Status == nil {
		_ = json.Unmarshal([]byte(defaultStatus), &server.Status) // Valid.
	}
	if s.Servers == nil {
		s.Servers = make(map[string]serverSettings)
	}
	s.Servers[defaultServerID] = server
}

// resolvePaths makes the relative paths in s, read from a configuration file
// in dir, relative to dir rather than to the working directory.
func (s *settings) resolvePaths(dir string) {
	for _, server := range s.Servers {
		fields := server.Status
		path, ok := fields["favicon"].(string)
		if ok && path != "" && !strings.HasPrefix(path, faviconDataPrefix) && !filepath.IsAbs(path) {
			fields["favicon"] = filepath.Join(dir, path)
		}
	}
}

// checker collects the mistakes found while checking settings, named after
// the key of their setting, such as "connections.max" or
// "servers.lobby.kick".
type checker struct {
	errs []error
}

func (c *checker) fail(key string, err error) {
	c.errs = append(c.errs, fmt.Errorf("%s: %w", key, err))
}

func (c *checker) failf(key, format string, args ...any) {
	c.fail(key, fmt.Errorf(format, args...))
}

// check checks the settings, and returns the configuration they make, with
// the defaults of the unset ones.
func (s *settings) check() (Config, []error) {
	var c checker
	var cfg Config
	cfg.Listeners = c.listeners(s.Listen)

	conns := s.Connections
	cfg.Server.ConnectionTimeout = c.duration("connections.timeout", conns.Timeout, defaultConnectionTimeout)
	cfg.Server.HandshakeTimeout = c.handshakeTimeout(conns.HandshakeTimeout, cfg.Server.ConnectionTimeout)
	cfg.Server.MaxConnections = c.count("connections.max", conns.Max, defaultMaxConnections)
	cfg.Server.MaxConnectionsPerIP = c.count("connections.max_per_ip", conns.MaxPerIP, defaultMaxConnectionsPerIP)

	cfg.LogFormat = c.logFormat(s.Log.Format)
	cfg.LogLevel = c.logLevel(s.Log.Level)
	cfg.Server.LogRateLimit = c.count("log.rate_limit", s.Log.RateLimit, defaultLogRateLimit)

	cfg.Server.SoftwareSignature = !s.NoSoftwareSignature
	cfg.Server.Servers = c.servers(s.Servers, cfg.Server.SoftwareSignature)
	cfg.Server.Rules = c.rules(s.Rules, cfg.Server.Servers)
	return cfg, c.errs
}

// duration returns the positive duration at key, or def if unset.
func (c *checker) duration(key string, d optional[time.Duration], def time.Duration) time.Duration {
	if d.set && d.value <= 0 {
		c.failf(key, "must be positive, got %s", d.value)
	}
	return d.or(def)
}

// count returns the count of things, such as connections, at key, or def if
// unset.
func (c *checker) count(key string, n optional[int], def int) int {
	if n.set && (n.value < 0 || n.value > math.MaxInt32) {
		c.failf(key, "must be between 0 and %d, got %d", math.MaxInt32, n.value)
	}
	return n.or(def)
}

// listeners returns the listeners, or the default one if there are none.
func (c *checker) listeners(entries []listenSettings) []Listener {
	if len(entries) == 0 {
		return []Listener{{Network: "tcp", Address: ":" + strconv.Itoa(defaultPort)}}
	}
	var listeners []Listener
	for i, entry := range entries {
		key := "listen." + strconv.Itoa(i)
		if entry.Address == "" {
			c.failf(key+".address", "must be set")
			continue
		}
		l, err := parseListenAddr(entry.Address)
		if err != nil {
			c.fail(key, err)
			continue
		}
		l.Options = server.ListenerOptions{
			ProxyProtocol:  entry.ProxyProtocol,
			TrustedProxies: c.trustedProxies(key+".trusted_proxies", entry, l.Network),
		}
		listeners = append(listeners, l)
	}
	return listeners
}

// handshakeTimeout returns the handshake timeout, which must not exceed the
// connection timeout.
func (c *checker) handshakeTimeout(d optional[time.Duration], connectionTimeout time.Duration) time.Duration {
	if d.set && (d.value <= 0 || d.value > connectionTimeout) {
		c.failf("connections.handshake_timeout", "must be positive and at most the connection timeout (%s), got %s", connectionTimeout, d.value)
	}
	// At most the connection timeout by default, so that a short one works
	// without also setting the handshake timeout.
	return d.or(min(defaultHandshakeTimeout, connectionTimeout))
}

// logFormat parses the name of a log format, in any case.
func (c *checker) logFormat(text string) logging.Format {
	if text == "" {
		return defaultLogFormat
	}
	format, err := logging.ParseFormat(text)
	if err != nil {
		c.fail("log.format", err)
	}
	return format
}

// logLevel parses the name of a log level, in any case. Unlike slog, it does
// not take offsets, such as "info+2", which users do not know.
func (c *checker) logLevel(text string) slog.Level {
	switch strings.ToLower(text) {
	case "":
		return defaultLogLevel
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	c.failf("log.level", "must be debug, info, warn or error, got %q", text)
	return defaultLogLevel
}

// trustedProxies returns the trusted proxies, at key, of the listener of
// entry on network.
func (c *checker) trustedProxies(key string, entry listenSettings, network string) []netip.Prefix {
	switch {
	case entry.TrustedProxies != nil && !entry.ProxyProtocol:
		c.failf(key, "requires proxy_protocol")
		return nil
	case entry.TrustedProxies != nil && network == "unix":
		c.failf(key, "does not apply to Unix sockets, whose peers have no address: proxy_protocol alone trusts them")
		return nil
	case len(entry.TrustedProxies) == 0 && entry.ProxyProtocol && network == "tcp":
		// Trusting any TCP peer must be explicit: anyone reaching the server
		// could then send a header with a forged address.
		c.failf(key, "must list the proxies allowed to send headers, such as 10.0.0.0/8, or 0.0.0.0/0 and ::/0 for any address")
		return nil
	}
	var prefixes []netip.Prefix
	for _, item := range entry.TrustedProxies {
		prefix, err := parsePrefix(item)
		if err != nil {
			c.fail(key, err)
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

// checkID checks the ID of a server, at key. IDs are limited to the characters
// of environment variable names, in lowercase, so that each one can be set by a
// variable.
func (c *checker) checkID(key, id string) {
	if !isID(id) {
		c.failf(key, "the ID must be made of lowercase letters, digits and underscores, got %q", id)
	}
}

func isID(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	}) < 0
}

// servers returns the virtual servers, the default one among them (see
// addDefaultServer). The default server answers what the others do not:
// statuses, through the implicit last rule, and joins to servers without a
// kick message.
func (c *checker) servers(entries map[string]serverSettings, signed bool) map[string]server.VirtualServer {
	ids := slices.Sorted(maps.Keys(entries))
	servers := make(map[string]server.VirtualServer, len(ids))
	for _, id := range ids {
		key, entry := "servers."+id, entries[id]
		vs := server.VirtualServer{ClientProtocol: c.clientProtocol(key+".client_protocol", entry.ClientProtocol)}
		c.checkID(key, id)
		if entry.Status == nil && entry.Kick == nil {
			c.failf(key, "must have a status or a kick message")
		}
		if entry.Status != nil {
			vs.Status = c.status(key+".status", entry.Status, vs.ClientProtocol, signed)
		}
		if entry.Kick != nil {
			vs.Kick = c.kick(key+".kick", entry.Kick)
		}
		servers[id] = vs
	}
	defaultKick := servers[defaultServerID].Kick
	if defaultKick == nil {
		defaultKick = c.kick("servers."+defaultServerID+".kick", defaultKickMessage)
	}
	for id, vs := range servers {
		if vs.Kick == nil {
			vs.Kick = defaultKick
			servers[id] = vs
		}
	}
	return servers
}

// clientProtocol parses the name of a server.ClientProtocol, in any case.
func (c *checker) clientProtocol(key, text string) server.ClientProtocol {
	switch strings.ToLower(text) {
	case "", "auto":
		return server.ClientProtocolAuto
	case "always":
		return server.ClientProtocolAlways
	case "never":
		return server.ClientProtocolNever
	}
	c.failf(key, "must be auto, always or never, got %q", text)
	return server.ClientProtocolAuto
}

// status returns the status at key made of fields, or nil if invalid. Its
// favicon may be the path of a PNG file, and its description, if text that
// starts with "{", is JSON (see jsonComponent).
func (c *checker) status(key string, fields statusSettings, clientProtocol server.ClientProtocol, signed bool) *status.Status {
	if favicon, ok := fields["favicon"]; ok {
		faviconKey := key + ".favicon"
		path, ok := favicon.(string)
		if !ok || path == "" {
			c.failf(faviconKey, "must be the path of a PNG file or a data URI, got %v", favicon)
			return nil
		}
		uri, err := loadFavicon(path)
		if err != nil {
			c.fail(faviconKey, err)
			return nil
		}
		fields = maps.Clone(fields)
		fields["favicon"] = uri
	}
	if text, ok := fields["description"].(string); ok && strings.HasPrefix(text, "{") {
		component, err := jsonComponent(text)
		if err != nil {
			c.fail(key+".description", err)
			return nil
		}
		fields = maps.Clone(fields)
		fields["description"] = component
	}
	data, err := json.Marshal(fields)
	if err != nil {
		c.fail(key, err)
		return nil
	}
	st, err := status.Parse(data)
	if err == nil {
		err = server.CheckStatusLength(st, clientProtocol, signed)
	}
	var fieldErr *status.FieldError
	switch {
	case errors.As(err, &fieldErr):
		c.fail(key+"."+fieldErr.Path.String(), fieldErr.Err)
		return nil
	case err != nil:
		c.fail(key, err)
		return nil
	}
	return st
}

// kick returns the kick message at key, a text component (see textComponent).
func (c *checker) kick(key string, message any) json.RawMessage {
	component, err := textComponent(message)
	if err == nil {
		err = server.CheckKickLength(component)
	}
	if err != nil {
		c.fail(key, err)
	}
	return component
}

// rules returns the rules, followed by the implicit last one, which targets
// the default server, so that every client gets an answer.
func (c *checker) rules(entries []ruleSettings, servers map[string]server.VirtualServer) []server.Rule {
	rules := make([]server.Rule, 0, len(entries)+1)
	names := make(map[string]int, len(entries))
	for i, entry := range entries {
		key := "rules." + strconv.Itoa(i)
		rule := server.Rule{Name: strconv.Itoa(i), Server: entry.Server}
		if entry.Name != "" {
			c.ruleName(key+".name", entry.Name)
			if other, ok := names[entry.Name]; ok {
				c.failf(key+".name", "rule %d has the same name", other)
			}
			names[entry.Name] = i
			rule.Name = entry.Name
		}
		if entry.Host != nil {
			if hosts, err := condition.Hosts(entry.Host); err != nil {
				c.fail(key+".host", err)
			} else {
				rule.Conditions = append(rule.Conditions, hosts)
			}
		}
		if _, ok := servers[entry.Server]; !ok {
			if entry.Server == "" {
				c.failf(key+".server", "must be set")
			} else {
				c.failf(key+".server", "no server has the ID %q", entry.Server)
			}
		}
		rules = append(rules, rule)
	}
	return append(rules, server.Rule{Name: defaultRuleName, Server: defaultServerID})
}

// ruleName checks the name of a rule, made like IDs, but not only of digits
// nor the name of the implicit last rule, so that logs name each rule once.
func (c *checker) ruleName(key, name string) {
	if !isID(name) {
		c.failf(key, "must be made of lowercase letters, digits and underscores, got %q", name)
	} else if strings.Trim(name, "0123456789") == "" {
		c.failf(key, "must not be only digits, which name the rules without a name, got %q", name)
	}
	if name == defaultRuleName {
		c.failf(key, "%q is the name of the implicit last rule", name)
	}
}

// parseListenAddr checks the syntax of a host:port or unix:path address. What
// depends on the system, such as whether the address is free, is checked when
// listening.
func parseListenAddr(s string) (Listener, error) {
	if path, ok := strings.CutPrefix(s, unixAddrPrefix); ok {
		if path == "" {
			return Listener{}, fmt.Errorf("%q has no socket path", s)
		}
		return Listener{Network: "unix", Address: path}, nil
	}
	_, port, err := net.SplitHostPort(s)
	if err != nil {
		return Listener{}, fmt.Errorf("%q must be host:port or %spath", s, unixAddrPrefix)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > math.MaxUint16 {
		return Listener{}, fmt.Errorf("%q has an invalid port, must be between 1 and %d", s, math.MaxUint16)
	}
	return Listener{Network: "tcp", Address: s}, nil
}

// ipv4MappedPrefixBits is the length of the prefix of IPv4-mapped IPv6
// addresses (::ffff:0:0/96), which hold the IPv4 address in their last 32 bits.
const ipv4MappedPrefixBits = 96

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
		// Client addresses with a zone never match a prefix.
		if addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("%q has an IPv6 zone, which is not supported", s)
		}
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

// textComponent returns the JSON text component of v: a component written in
// YAML, or text, which is JSON if it starts with "{" (see jsonComponent) and
// plain text otherwise.
func textComponent(v any) (json.RawMessage, error) {
	switch v := v.(type) {
	case string:
		if strings.HasPrefix(v, "{") {
			return jsonComponent(v)
		}
		return json.Marshal(map[string]string{"text": v})
	case map[string]any, []any:
		return json.Marshal(v)
	default:
		return nil, fmt.Errorf("must be text or a text component, got %v", v)
	}
}

// jsonComponent returns text, a text component written in JSON. It must be
// valid, so that a mistake does not show the JSON to players as plain text.
func jsonComponent(text string) (json.RawMessage, error) {
	// Decoding into any only fails on syntax errors.
	var syntaxErr *json.SyntaxError
	if err := json.Unmarshal([]byte(text), new(any)); errors.As(err, &syntaxErr) {
		return nil, fmt.Errorf(`invalid JSON at byte %d: %w (for plain text that starts with "{", write { text: "..." })`, syntaxErr.Offset, err)
	}
	return json.RawMessage(text), nil
}

// faviconDataPrefix starts the favicons that are data URIs rather than paths.
const faviconDataPrefix = "data:"

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// loadFavicon returns value as is if it is already a data URI, and otherwise
// reads the PNG file it points to.
func loadFavicon(value string) (string, error) {
	if strings.HasPrefix(value, faviconDataPrefix) {
		return value, nil
	}
	data, err := os.ReadFile(value)
	if err != nil {
		return "", err
	}
	if !bytes.HasPrefix(data, pngSignature) {
		return "", fmt.Errorf("%s is not a PNG file", value)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), nil
}
