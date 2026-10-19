package script

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/starlark"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// hook is a script that answers the requests to a server: an expression that
// sees the client, ctx and the server.
type hook struct {
	scope  *Scope
	server *scopeServer
	fn     *starlark.Function
}

// compileHook compiles src, the hook of the server with the ID server.
func (s *Scope) compileHook(src, server string) (hook, error) {
	srv, ok := s.servers[server]
	if !ok {
		return hook{}, fmt.Errorf("no server has the ID %q", server)
	}
	fn, err := s.compileExpr(src, "client", "ctx", "server")
	if err != nil {
		return hook{}, err
	}
	return hook{scope: s, server: srv, fn: fn}, nil
}

// run runs h for req, with its prints going to printer, and returns its result,
// and the client value, which tells the fields that it read.
func (h *hook) run(req *Request, printer func(msg string)) (starlark.Value, *clientValue, error) {
	v := clientKind.new(req.Client)
	srv := serverKind.new(serverView{scope: h.scope, srv: h.server, req: req})
	result, err := starlark.Call(newThread(printer), h.fn, starlark.Tuple{v, h.scope.ctxValue(req), srv}, nil)
	if err != nil {
		return nil, v, errors.New(evalMessage(err))
	}
	return result, v, nil
}

// resultDict returns the members of result, the result of a hook that is None
// or a dict whose keys are among keys, as want describes it. It returns nil
// for None.
func resultDict(result starlark.Value, want string, keys ...string) (map[string]starlark.Value, error) {
	if result == starlark.None {
		return nil, nil
	}
	d, ok := result.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("must return None or %s, got %s", want, result.Type())
	}
	members := make(map[string]starlark.Value, d.Len())
	for _, item := range d.Items() {
		key, ok := item[0].(starlark.String)
		if !ok || !slices.Contains(keys, string(key)) {
			return nil, fmt.Errorf("unknown key %s in the result, want %s", item[0], want)
		}
		members[string(key)] = item[1]
	}
	return members, nil
}

// JoinHook is the join_script of a server: it can answer the players who try
// to join the server in place of its kick message.
type JoinHook struct {
	hook
	check func(kick json.RawMessage) error
}

// CompileJoinHook returns the join hook written in src, a Starlark expression
// that sees client, ctx and server, of the server with the ID server. The hook
// returns None, or {"kick": message}, with message plain text, or a text
// component written as dicts and lists, which check checks, such as its
// length. It is checked like conditions (see CompileCondition), with the
// samples of players joining.
func (s *Scope) CompileJoinHook(src, server string, check func(kick json.RawMessage) error) (*JoinHook, error) {
	compiled, err := s.compileHook(src, server)
	if err != nil {
		return nil, err
	}
	h := &JoinHook{hook: compiled, check: check}
	if err := checkSamples(samplesOf(RequestJoin), func(req *Request) error {
		_, err := h.Run(req, nil)
		return err
	}); err != nil {
		return nil, err
	}
	return h, nil
}

// Run runs h for req, and returns the kick message that it answers, a JSON text
// component, or nil if it returns None. Its errors are *Error. What it prints
// goes to printer, or nowhere if nil.
func (h *JoinHook) Run(req *Request, printer func(msg string)) (json.RawMessage, error) {
	result, v, err := h.run(req, printer)
	var kick json.RawMessage
	if err == nil {
		kick, err = h.kick(result)
	}
	if err != nil {
		return nil, &Error{Err: err, Fields: clientFields(v)}
	}
	return kick, nil
}

func (h *JoinHook) kick(result starlark.Value) (json.RawMessage, error) {
	members, err := resultDict(result, `{"kick": message}`, "kick")
	if err != nil || members == nil {
		return nil, err
	}
	var kick json.RawMessage
	switch message := members["kick"].(type) {
	case starlark.String:
		// Text is never parsed as JSON, so that the values of clients in it
		// stay text.
		kick = status.Object{{Name: "text", Value: string(message)}}.AppendJSON(nil)
	case *starlark.Dict, *starlark.List, starlark.Tuple:
		if kick, err = marshalJSON(message); err != nil {
			return nil, fmt.Errorf(`"kick": %w`, err)
		}
	default:
		return nil, fmt.Errorf(`"kick" must be text or a text component, got %s`, message.Type())
	}
	if err := h.check(kick); err != nil {
		return nil, fmt.Errorf(`"kick": %w`, err)
	}
	return kick, nil
}

// PingHook is the ping_script of a server: it can answer the status requests
// to the server in place of its status.
type PingHook struct {
	hook
	check func(st *status.Status, clientProtocol status.ClientProtocol) error
}

// PingAnswer is the answer of a ping hook: a status, and the client protocol
// to answer it with, the one that the hook returned, or else the one of the
// server.
type PingAnswer struct {
	Status         *status.Status
	ClientProtocol status.ClientProtocol
}

// CompilePingHook returns the ping hook written in src, a Starlark expression
// that sees client, ctx and server, of the server with the ID server. The hook
// returns None, or {"status": status}, with status the whole status, and
// optionally "client_protocol", which check checks, such as the length of the
// status. It is checked like conditions (see CompileCondition), with the
// samples of status requests.
func (s *Scope) CompilePingHook(src, server string, check func(st *status.Status, clientProtocol status.ClientProtocol) error) (*PingHook, error) {
	compiled, err := s.compileHook(src, server)
	if err != nil {
		return nil, err
	}
	h := &PingHook{hook: compiled, check: check}
	if err := checkSamples(samplesOf(RequestPing), func(req *Request) error {
		_, err := h.Run(req, nil)
		return err
	}); err != nil {
		return nil, err
	}
	return h, nil
}

// Run runs h for req, and returns its answer, or nil if it returns None. Its
// errors are *Error. What it prints goes to printer, or nowhere if nil.
func (h *PingHook) Run(req *Request, printer func(msg string)) (*PingAnswer, error) {
	result, v, err := h.run(req, printer)
	var answer *PingAnswer
	if err == nil {
		answer, err = h.answer(result)
	}
	if err != nil {
		return nil, &Error{Err: err, Fields: clientFields(v)}
	}
	return answer, nil
}

func (h *PingHook) answer(result starlark.Value) (*PingAnswer, error) {
	members, err := resultDict(result, `{"status": status}`, "status", "client_protocol")
	if err != nil || members == nil {
		return nil, err
	}
	answer := &PingAnswer{ClientProtocol: h.server.info.ClientProtocol}
	if value, ok := members["client_protocol"]; ok {
		text, _ := value.(starlark.String)
		if answer.ClientProtocol, ok = status.ParseClientProtocol(string(text)); !ok {
			return nil, fmt.Errorf(`"client_protocol" must be "auto", "always" or "never", got %s`, value)
		}
	}
	value, ok := members["status"]
	if !ok {
		return nil, errors.New(`the result has no "status"`)
	}
	fields, ok := value.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf(`"status" must be a dict, got %s`, value.Type())
	}
	if favicon, found, _ := fields.Get(starlark.String("favicon")); found {
		// Not a file, which would be read at each request, nor another format,
		// which clients ignore. Its size is not checked: hooks may pass on the
		// favicon of an upstream, whatever its size.
		if text, ok := favicon.(starlark.String); !ok || !strings.HasPrefix(string(text), status.FaviconPrefix) {
			return nil, fmt.Errorf(`"status": favicon must be a PNG data URI (%q...), got %s`, status.FaviconPrefix, favicon)
		}
	}
	converted, err := toJSON(fields, 0)
	if err != nil {
		return nil, fmt.Errorf(`"status": %w`, err)
	}
	object := converted.(status.Object)
	if i := slices.IndexFunc(object, func(m status.Member) bool { return m.Name == "favicon" }); i >= 0 {
		if encoded := h.scope.encodedFavicon(object[i].Value.(string)); encoded != nil {
			object[i].Value = encoded
		}
	}
	if answer.Status, err = status.New(object); err != nil {
		var fieldErr *status.FieldError
		if errors.As(err, &fieldErr) {
			return nil, fmt.Errorf(`"status": %s: %w`, fieldErr.Path, fieldErr.Err)
		}
		return nil, fmt.Errorf(`"status": %w`, err)
	}
	if err := h.check(answer.Status, answer.ClientProtocol); err != nil {
		return nil, fmt.Errorf(`"status": %w`, err)
	}
	return answer, nil
}
