package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/condition"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// VirtualServer is a server as clients see it: its status in their server
// list, and what happens when they try to join it.
type VirtualServer struct {
	// Status is the status in the server list.
	Status         *status.Status
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

// serverFor returns the virtual server that answers the client of sess: the
// one of the first rule that applies, whatever the request. It records the
// rule and the server in sess.
func (s *Server) serverFor(sess *session) VirtualServer {
	for _, rule := range s.cfg.Rules {
		if rule.matches(sess) {
			sess.rule, sess.server = rule.Name, rule.Server
			return s.cfg.Servers[rule.Server]
		}
	}
	panic("no rule applies") // The last rule always applies (see checkServers).
}

// statusFor returns the status to answer to the client of sess, the one of its
// server (see serverFor), with the protocol version of the client if the
// server reports it.
func (s *Server) statusFor(sess *session) *status.Status {
	server := s.serverFor(sess)
	st := server.Status
	if protocolVersion, ok := clientProtocolVersion(sess); ok && server.ClientProtocol.replaces(st) {
		st = st.WithProtocolVersion(protocolVersion)
	}
	return st
}

// checkServers checks the virtual servers, which must all have a status and a
// kick message, and the rules, which must all target one. The last rule must
// have no conditions, so that every client gets an answer.
func checkServers(cfg *Config) error {
	if len(cfg.Rules) == 0 {
		return errors.New("no rules")
	}
	for i, rule := range cfg.Rules {
		if _, ok := cfg.Servers[rule.Server]; !ok {
			return fmt.Errorf("rule %d: no server %q", i, rule.Server)
		}
		if i == len(cfg.Rules)-1 && len(rule.Conditions) != 0 {
			return fmt.Errorf("rule %d: the last rule must have no conditions, so that it always applies", i)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(cfg.Servers)) {
		server := cfg.Servers[id]
		if server.Status == nil {
			return fmt.Errorf("server %s: no status", id)
		}
		if err := CheckStatusLength(server.Status, server.ClientProtocol, cfg.SoftwareSignature); err != nil {
			return fmt.Errorf("server %s: status: %w", id, err)
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
