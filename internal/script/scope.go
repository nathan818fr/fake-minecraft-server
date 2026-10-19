package script

import (
	"encoding/json"
	"maps"
	"slices"

	"go.starlark.net/starlark"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// UpstreamInfo is an upstream of a configuration, as scripts see it.
type UpstreamInfo struct {
	ID string
	// Address is host or host:port, as configured.
	Address string
}

// ServerInfo is a server of a configuration, as scripts see it.
type ServerInfo struct {
	ID string
	// StaticStatus is the status of the server while its upstream, if any, is
	// unavailable, and Upstream the ID of that upstream, or "".
	StaticStatus *status.Status
	Upstream     string
	// ClientProtocol is the one that ping hooks answer with, unless they
	// return another.
	ClientProtocol status.ClientProtocol
	// Kick is a JSON text component.
	Kick json.RawMessage
}

// Scope is what the scripts of a configuration see besides the client: the
// globals of its module, its upstreams and its servers, whose IDs are checked
// when compiling.
type Scope struct {
	module *module
	// moduleFailed reports that its module failed to load, so that the
	// scripts that use its names are not reported as typos.
	moduleFailed bool
	// prints are what the top level of its module printed (see Prints).
	prints    []string
	upstreams map[string]*scopeUpstream
	// upstreamList and serverList hold them sorted by ID, for ctx.upstreams
	// and ctx.servers.
	upstreamList []*scopeUpstream
	servers      map[string]*scopeServer
	serverList   []*scopeServer
	// favicons are the favicons of the static statuses and the ones that the
	// module read, which do not change (see encodedFavicon).
	favicons []encodedFavicon
}

type scopeUpstream struct {
	info UpstreamInfo
	// status holds the Starlark value of its last status (see statusSlot).
	status statusSlot
}

type scopeServer struct {
	info ServerInfo
	// staticStatus and kick are the frozen Starlark values of info, made once.
	staticStatus starlark.Value
	kick         starlark.Value
}

// NewScope returns the scope of a configuration with the given upstreams and
// servers, without module (see LoadModule).
func NewScope(upstreams []UpstreamInfo, servers []ServerInfo) *Scope {
	s := &Scope{
		upstreams: make(map[string]*scopeUpstream, len(upstreams)),
		servers:   make(map[string]*scopeServer, len(servers)),
	}
	for _, u := range upstreams {
		s.upstreams[u.ID] = &scopeUpstream{info: u}
	}
	for _, id := range slices.Sorted(maps.Keys(s.upstreams)) {
		s.upstreamList = append(s.upstreamList, s.upstreams[id])
	}
	for _, info := range servers {
		kick := componentValue(info.Kick)
		kick.Freeze()
		srv := &scopeServer{info: info, staticStatus: statusValue(info.StaticStatus), kick: kick}
		s.servers[info.ID] = srv
		if info.StaticStatus != nil {
			if f := faviconOf(info.StaticStatus, srv.staticStatus); f.json != nil {
				s.favicons = append(s.favicons, f)
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.servers)) {
		s.serverList = append(s.serverList, s.servers[id])
	}
	return s
}

// encodedFavicon returns the JSON of favicon if it is one that scripts can read
// (see encodedFavicon.is): of a static status, read by the module, or of the
// last status of an upstream. It returns nil otherwise.
func (s *Scope) encodedFavicon(favicon string) json.RawMessage {
	for _, f := range s.favicons {
		if f.is(favicon) {
			return f.json
		}
	}
	for _, u := range s.upstreamList {
		if c := u.status.last.Load(); c != nil && c.favicon.is(favicon) {
			return c.favicon.json
		}
	}
	return nil
}

// componentValue returns component, a JSON text component, as a Starlark
// value, or None if it is missing or invalid.
func componentValue(component json.RawMessage) starlark.Value {
	if component == nil {
		return starlark.None
	}
	v, err := status.ParseValue(component)
	if err != nil {
		return starlark.None
	}
	return jsonValue(v)
}

// upstreamView is an upstream during a request: the state of the objects of
// upstreamKind, which ctx.upstream and ctx.upstreams give.
type upstreamView struct {
	u   *scopeUpstream
	req *Request
}

// upstreamKind is the kind of the upstreams of scripts. Its fields that depend
// on the state of the upstream read it only if asked, so that scripts that only
// list upstreams never depend on them.
var upstreamKind = newObjectKind("upstream", func(v upstreamView) string {
	return "upstream(id = " + starlark.String(v.u.info.ID).String() + ")"
}, []objectAttr[upstreamView]{
	{name: "address", field: func(v upstreamView) starlark.Value { return starlark.String(v.u.info.Address) }},
	// Since the last successful poll, and its latency below, None before any.
	{name: "age_ms", field: func(v upstreamView) starlark.Value {
		state := v.req.upstreamState(v.u.info.ID)
		if !state.Polled {
			return starlark.None
		}
		return starlark.Float(state.Age.Seconds() * 1000)
	}},
	{name: "id", field: func(v upstreamView) starlark.Value { return starlark.String(v.u.info.ID) }},
	{name: "latency_ms", field: func(v upstreamView) starlark.Value {
		state := v.req.upstreamState(v.u.info.ID)
		if !state.Polled {
			return starlark.None
		}
		return starlark.Float(state.Latency.Seconds() * 1000)
	}},
	{name: "status", field: func(v upstreamView) starlark.Value {
		return v.u.status.value(v.req.upstreamState(v.u.info.ID).Status)
	}},
})

// serverView is a server during a request: the state of the objects of
// serverKind, the server variable of hooks and what ctx.server and
// ctx.servers give.
type serverView struct {
	scope *Scope
	srv   *scopeServer
	req   *Request
}

// serverKind is the kind of the servers of scripts. Its status is the one that
// it shows without hooks: the one of its upstream while available, read only if
// asked, so that scripts that do not read it never depend on upstreams.
var serverKind = newObjectKind("server", func(v serverView) string {
	return "server(id = " + starlark.String(v.srv.info.ID).String() + ")"
}, []objectAttr[serverView]{
	{name: "id", field: func(v serverView) starlark.Value { return starlark.String(v.srv.info.ID) }},
	{name: "kick", field: func(v serverView) starlark.Value { return v.srv.kick }},
	{name: "static_status", field: func(v serverView) starlark.Value { return v.srv.staticStatus }},
	{name: "status", field: func(v serverView) starlark.Value {
		if u, ok := v.scope.upstreams[v.srv.info.Upstream]; ok {
			if st := v.req.upstreamState(u.info.ID).Status; st != nil {
				return u.status.value(st)
			}
		}
		return v.srv.staticStatus
	}},
	{name: "upstream", field: func(v serverView) starlark.Value { return starlark.String(v.srv.info.Upstream) }},
})
