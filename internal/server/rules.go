package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/nathan818fr/fake-minecraft-server/internal/condition"
	"github.com/nathan818fr/fake-minecraft-server/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/internal/status"
)

// VirtualServer is a server as clients see it: its status in their server
// list, and what happens when they try to join it.
type VirtualServer struct {
	// Status is the static status, answered while the upstream has none, or
	// nil.
	Status *status.Status
	// Upstream is the ID of the upstream giving the live status, if any.
	Upstream       string
	ClientProtocol ClientProtocol
	// Kick is the message sent to players who try to join, a JSON text
	// component.
	Kick json.RawMessage
}

// Rule tells which virtual server clients see. The rules are tried in order,
// and apply to the clients that match all their conditions.
type Rule struct {
	// Name identifies the rule in logs.
	Name       string
	Conditions []condition.Condition
	Server     string
}

// matches reports whether the client of sess matches all the conditions of r.
func (r Rule) matches(sess *session) bool {
	for _, cond := range r.Conditions {
		if !cond.Match(sess.conditionClient()) {
			return false
		}
	}
	return true
}

// ClientProtocol is when to report the protocol version of each client in
// place of the one of the status, so that clients see the server as
// compatible.
type ClientProtocol int

const (
	// ClientProtocolAuto keeps the protocol versions that no client has (see
	// firstClientProtocolVersion).
	ClientProtocolAuto ClientProtocol = iota
	ClientProtocolAlways
	ClientProtocolNever
)

// firstClientProtocolVersion is the protocol version of 1.7.2, the first
// release with the modern protocol. Lower ones make clients show the status as
// incompatible, with its version name in place of the player counts: a common
// way to show a message such as "§cMaintenance", with -1 or 1 in plugins.
const firstClientProtocolVersion = 4

// replaces reports whether the protocol version of st is replaced by the one
// of each client.
func (c ClientProtocol) replaces(st *status.Status) bool {
	switch c {
	case ClientProtocolAlways:
		return true
	case ClientProtocolNever:
		return false
	default:
		protocolVersion, ok := st.ProtocolVersion()
		return !ok || protocolVersion >= firstClientProtocolVersion
	}
}

// statusSource is where the status answered to a client came from, as logged.
type statusSource string

const (
	sourceStatic   statusSource = "static"
	sourceUpstream statusSource = "upstream"
)

// status returns the status of server right now: the one of its upstream while
// it has one, or else its static one, or nil if neither.
func (s *Server) status(server VirtualServer) (*status.Status, statusSource) {
	if upstream, ok := s.upstreams[server.Upstream]; ok {
		if st := upstream.Status(); st != nil {
			return st, sourceUpstream
		}
	}
	return server.Status, sourceStatic
}

// statusFor returns the status to answer to the client of sess: the one of the
// server of the first rule that applies and has one right now. It records the
// rule and the server in sess.
func (s *Server) statusFor(sess *session) *status.Status {
	for _, rule := range s.cfg.Rules {
		if !rule.matches(sess) {
			continue
		}
		server := s.cfg.Servers[rule.Server]
		st, source := s.status(server)
		if st == nil {
			continue
		}
		sess.rule, sess.server, sess.statusSource = rule.Name, rule.Server, source
		if protocolVersion, ok := clientProtocolVersion(sess); ok && server.ClientProtocol.replaces(st) {
			st = st.WithProtocolVersion(protocolVersion)
		}
		return st
	}
	panic("no rule applies") // The last rule always applies (see checkServers).
}

// kickFor returns the kick message for the client of sess: the one of the
// server of the first rule that applies, as every server answers joins. It
// records the rule and the server in sess.
func (s *Server) kickFor(sess *session) json.RawMessage {
	for _, rule := range s.cfg.Rules {
		if rule.matches(sess) {
			sess.rule, sess.server = rule.Name, rule.Server
			return s.cfg.Servers[rule.Server].Kick
		}
	}
	panic("no rule applies") // The last rule always applies (see checkServers).
}

// checkServers checks the virtual servers, whose upstreams must be in
// upstreams, and the rules, which must all target one. The last rule must
// always apply, without conditions and with a server that has a static status,
// so that every client gets an answer.
func checkServers(cfg *Config, upstreams map[string]Upstream) error {
	if len(cfg.Rules) == 0 {
		return errors.New("no rules")
	}
	for i, rule := range cfg.Rules {
		server, ok := cfg.Servers[rule.Server]
		if !ok {
			return fmt.Errorf("rule %d: no server %q", i, rule.Server)
		}
		if i == len(cfg.Rules)-1 && (len(rule.Conditions) != 0 || server.Status == nil) {
			return fmt.Errorf("rule %d: the last rule must have no conditions, and a server with a static status, so that it always applies", i)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(cfg.Servers)) {
		server := cfg.Servers[id]
		if _, ok := upstreams[server.Upstream]; !ok && server.Upstream != "" {
			return fmt.Errorf("server %s: no upstream %q", id, server.Upstream)
		}
		if server.Status != nil {
			if err := CheckStatusLength(server.Status, server.ClientProtocol, cfg.SoftwareSignature); err != nil {
				return fmt.Errorf("server %s: status: %w", id, err)
			}
		}
		if server.Kick == nil {
			return fmt.Errorf("server %s: no kick message", id)
		}
		if err := CheckKickLength(server.Kick); err != nil {
			return fmt.Errorf("server %s: kick: %w", id, err)
		}
	}
	return nil
}

// CheckKickLength checks that kick fits in a disconnect packet, which clients
// would reject otherwise.
func CheckKickLength(kick json.RawMessage) error {
	if n := modernproto.UTF16Length(kick); n > modernproto.MaxDisconnectReasonLength {
		return fmt.Errorf("too long: %d characters, the limit is %d", n, modernproto.MaxDisconnectReasonLength)
	}
	return nil
}
