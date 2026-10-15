package script

import (
	"fmt"

	"go.starlark.net/starlark"
)

// ctxValue is the ctx variable of scripts: the state of the request that they
// run for, which they reach through its methods, so that the fms functions stay
// pure. There is one per request, for its rules and hooks (see Request.ctx).
type ctxValue = object[ctxView]

// ctxView is the state of a ctxValue: a request, with the scope of its scripts.
type ctxView struct {
	scope *Scope
	req   *Request
}

// ctxKind is the kind of ctxValue: methods only.
var ctxKind = newObjectKind("ctx", func(ctxView) string { return "ctx" }, []objectAttr[ctxView]{
	{name: "server", method: ctxView.serverMethod},
	{name: "upstream", method: ctxView.upstreamMethod},
})

// ctxCallChecks are the checks of the calls to the methods of ctx (see
// callCheck): IDs that no upstream or server has.
var ctxCallChecks = map[string]callCheck{
	"upstream": {arg: 0, literal: func(s *Scope, id string) error {
		if _, ok := s.upstreams[id]; !ok {
			return fmt.Errorf("no upstream has the ID %q", id)
		}
		return nil
	}},
	"server": {arg: 0, literal: func(s *Scope, id string) error {
		if _, ok := s.servers[id]; !ok {
			return fmt.Errorf("no server has the ID %q", id)
		}
		return nil
	}},
}

// ctxValue returns the ctx variable of req, made when first used.
func (s *Scope) ctxValue(req *Request) *ctxValue {
	if req.ctx == nil {
		req.ctx = ctxKind.new(ctxView{scope: s, req: req})
	}
	return req.ctx
}

// upstreamMethod implements ctx.upstream(id), the state of an upstream during
// the request (see scopeUpstream.value).
func (v ctxView) upstreamMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &id); err != nil {
		return nil, err
	}
	u, ok := v.scope.upstreams[id]
	if !ok {
		return nil, fmt.Errorf("%s: no upstream has the ID %q", b.Name(), id)
	}
	return u.value(v.req), nil
}

// serverMethod implements ctx.server(id), a server of the configuration during
// the request (see serverKind).
func (v ctxView) serverMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &id); err != nil {
		return nil, err
	}
	srv, ok := v.scope.servers[id]
	if !ok {
		return nil, fmt.Errorf("%s: no server has the ID %q", b.Name(), id)
	}
	return serverKind.new(serverView{scope: v.scope, srv: srv, req: v.req}), nil
}
