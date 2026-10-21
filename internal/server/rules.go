package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/script"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// VirtualServer is a server as clients see it: its status in their server
// list, and what happens when they try to join it.
type VirtualServer struct {
	// Status is the static status, answered while the upstream has none.
	Status *status.Status
	// Upstream is the ID of the upstream giving the live status, if any.
	Upstream       string
	ClientProtocol status.ClientProtocol
	// Kick is the message sent to players who try to join, a JSON text
	// component.
	Kick json.RawMessage
	// PingHook and JoinHook, its ping_script and join_script if set, can
	// answer status requests and joins in place of the status and the kick
	// message.
	PingHook *script.PingHook
	JoinHook *script.JoinHook
}

// Rule tells which virtual server clients see. The rules are tried in order,
// and apply to the clients that match all their conditions.
type Rule struct {
	// Name identifies the rule in logs.
	Name       string
	Conditions []script.Condition
	Server     string
}

// matches reports whether the client of sess matches all the conditions of r.
// A condition that fails does not match, and its error is recorded in sess.
// What conditions print is logged with r (see Server.scriptPrinter).
func (s *Server) matches(r Rule, sess *session) bool {
	if len(r.Conditions) == 0 {
		return true
	}
	req := s.scriptRequest(sess)
	printer := s.scriptPrinter(&sess.conn, slog.String("rule", r.Name))
	for _, cond := range r.Conditions {
		ok, err := cond.Match(req, printer)
		if err != nil {
			sess.ruleErrors = append(sess.ruleErrors, ruleError{rule: r.Name, err: err})
		}
		if !ok {
			return false
		}
	}
	return true
}

// ruleError is the error of a rule whose condition failed.
type ruleError struct {
	rule string
	err  error
}

// answerSource is where the answer to a client came from, as logged.
type answerSource string

const (
	sourceStatic   answerSource = "static"
	sourceUpstream answerSource = "upstream"
	sourceScript   answerSource = "script"
)

// status returns the status of server for the request of sess: the one of its
// upstream while it has one, or else its static one.
func (s *Server) status(sess *session, server VirtualServer) (*status.Status, answerSource) {
	if st := s.upstreamState(sess, server.Upstream).Status; st != nil {
		return st, sourceUpstream
	}
	return server.Status, sourceStatic
}

// serverFor returns the virtual server that answers the client of sess: the
// one of the first rule that applies, whatever the request. It records the
// rule and the server in sess.
func (s *Server) serverFor(sess *session) VirtualServer {
	cfg := &sess.state.cfg
	for _, rule := range cfg.Rules {
		if s.matches(rule, sess) {
			sess.rule, sess.server = rule.Name, rule.Server
			return cfg.Servers[rule.Server]
		}
	}
	panic("no rule applies") // The last rule always applies (see checkServers).
}

// statusFor returns the status to answer to the client of sess: the one of its
// server (see serverFor), from its ping hook, or else from its upstream or
// static status, with the protocol version of the client if the server
// reports it. It records where the status came from in sess.
func (s *Server) statusFor(sess *session) *status.Status {
	server := s.serverFor(sess)
	st, source := s.status(sess, server)
	clientProtocol := server.ClientProtocol
	if server.PingHook != nil {
		answer, err := server.PingHook.Run(s.scriptRequest(sess), s.hookPrinter(sess, "ping_script"))
		switch {
		case err != nil:
			sess.hookErrors = append(sess.hookErrors, hookError{hook: "ping_script", server: sess.server, err: err})
		case answer != nil:
			st, source, clientProtocol = answer.Status, sourceScript, answer.ClientProtocol
		}
	}
	sess.source = source
	if protocolVersion, ok := clientProtocolVersion(sess); ok && clientProtocol.Replaces(st) {
		st = st.WithProtocolVersion(protocolVersion)
	}
	return st
}

// kickFor returns the kick message to answer to the client of sess: the one of
// its server (see serverFor), from its join hook, or else its own. It records
// where it came from in sess, if from the hook.
func (s *Server) kickFor(sess *session) json.RawMessage {
	server := s.serverFor(sess)
	if server.JoinHook != nil {
		kick, err := server.JoinHook.Run(s.scriptRequest(sess), s.hookPrinter(sess, "join_script"))
		switch {
		case err != nil:
			sess.hookErrors = append(sess.hookErrors, hookError{hook: "join_script", server: sess.server, err: err})
		case kick != nil:
			sess.source = sourceScript
			return kick
		}
	}
	return server.Kick
}

// hookPrinter returns the function that logs what the hook named hook of the
// server of sess prints, with them.
func (s *Server) hookPrinter(sess *session, hook string) func(msg string) {
	return s.scriptPrinter(&sess.conn, slog.String("server", sess.server), slog.String("hook", hook))
}

// hookError is the error of a hook that failed, which the server answered
// without.
type hookError struct {
	hook   string
	server string
	err    error
}

// checkServers checks the virtual servers, which must all have a static status
// and a kick message, and whose upstreams must be in cfg, and the rules, which
// must all target one. The last rule must have no conditions, so that every
// client gets an answer.
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
		if _, ok := cfg.Upstreams[server.Upstream]; !ok && server.Upstream != "" {
			return fmt.Errorf("server %s: no upstream %q", id, server.Upstream)
		}
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
