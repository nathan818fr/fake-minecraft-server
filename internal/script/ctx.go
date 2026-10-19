package script

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	startime "go.starlark.net/lib/time"
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
	{name: "choice", method: ctxView.choiceMethod},
	{name: "now", method: ctxView.nowMethod},
	{name: "randint", method: ctxView.randintMethod},
	{name: "random", method: ctxView.randomMethod},
	{name: "server", method: ctxView.serverMethod},
	{name: "servers", method: ctxView.serversMethod},
	{name: "upstream", method: ctxView.upstreamMethod},
	{name: "upstreams", method: ctxView.upstreamsMethod},
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
// the request (see upstreamKind).
func (v ctxView) upstreamMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &id); err != nil {
		return nil, err
	}
	u, ok := v.scope.upstreams[id]
	if !ok {
		return nil, fmt.Errorf("%s: no upstream has the ID %q", b.Name(), id)
	}
	return upstreamKind.new(upstreamView{u: u, req: v.req}), nil
}

// upstreamsMethod implements ctx.upstreams(), the states of all the upstreams
// during the request, sorted by ID. Each is read only when used.
func (v ctxView) upstreamsMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	values := make([]starlark.Value, len(v.scope.upstreamList))
	for i, u := range v.scope.upstreamList {
		values[i] = upstreamKind.new(upstreamView{u: u, req: v.req})
	}
	return starlark.NewList(values), nil
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

// serversMethod implements ctx.servers(), all the servers of the configuration
// during the request, sorted by ID.
func (v ctxView) serversMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	values := make([]starlark.Value, len(v.scope.serverList))
	for i, srv := range v.scope.serverList {
		values[i] = serverKind.new(serverView{scope: v.scope, srv: srv, req: v.req})
	}
	return starlark.NewList(values), nil
}

// nowMethod implements ctx.now(), the time of the request in UTC: the same at
// each call during the request, like the states of upstreams.
func (v ctxView) nowMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	req := v.req
	if req.now.IsZero() {
		if req.Now != nil {
			req.now = req.Now()
		} else {
			req.now = time.Now()
		}
		req.now = req.now.UTC()
	}
	return startime.Time(req.now), nil
}

// randomMethod implements ctx.random(), a random float in [0, 1).
func (v ctxView) randomMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	if r := v.req.Rand; r != nil {
		return starlark.Float(r.Float64()), nil
	}
	return starlark.Float(rand.Float64()), nil
}

// randintMethod implements ctx.randint(a, b), a random int in [a, b], like
// Python.
func (v ctxView) randintMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var lo, hi int64
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &lo, &hi); err != nil {
		return nil, err
	}
	if lo > hi {
		return nil, fmt.Errorf("%s: empty range: %d > %d", b.Name(), lo, hi)
	}
	// The span fits in a uint64, whatever the bounds.
	span := uint64(hi) - uint64(lo)
	var n uint64
	if span == math.MaxUint64 {
		n = v.uint64N(0)
	} else {
		n = v.uint64N(span + 1)
	}
	return starlark.MakeInt64(lo + int64(n)), nil
}

// choiceMethod implements ctx.choice(seq), a random element of seq, a list or a
// tuple. Not a string, which Starlark indexes by byte: its characters would
// be split.
func (v ctxView) choiceMethod(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &value); err != nil {
		return nil, err
	}
	var seq starlark.Indexable
	switch value := value.(type) {
	case *starlark.List:
		seq = value
	case starlark.Tuple:
		seq = value
	default:
		return nil, fmt.Errorf("%s: got %s, want a list or a tuple", b.Name(), value.Type())
	}
	if seq.Len() == 0 {
		return nil, fmt.Errorf("%s: empty %s", b.Name(), value.Type())
	}
	return seq.Index(int(v.uint64N(uint64(seq.Len())))), nil
}

// uint64N returns a random number in [0, n), or any uint64 if n is 0.
func (v ctxView) uint64N(n uint64) uint64 {
	r := v.req.Rand
	switch {
	case n == 0 && r != nil:
		return r.Uint64()
	case n == 0:
		return rand.Uint64()
	case r != nil:
		return r.Uint64N(n)
	}
	return rand.Uint64N(n)
}
