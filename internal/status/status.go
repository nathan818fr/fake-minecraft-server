// Package status holds server list statuses: the JSON objects that servers
// answer to status requests, with their version, players, MOTD (description)
// and favicon, and any other field that clients or mods read, such as
// enforcesSecureChat or forgeData.
package status

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// Status is a server list status. It is kept as a JSON object, so that all its
// fields, known or not, can be read, merged and transformed the same way. Code
// that changes the fields that this server reads must keep them valid, or
// check them again (see Check).
type Status struct {
	Object
}

var (
	versionPath         = Path{"version"}
	versionNamePath     = Path{"version", "name"}
	versionProtocolPath = Path{"version", "protocol"}
	playersPath         = Path{"players"}
	playersMaxPath      = Path{"players", "max"}
	playersOnlinePath   = Path{"players", "online"}
	descriptionPath     = Path{"description"}
	faviconPath         = Path{"favicon"}
)

// Parse parses the JSON of a status, and checks it.
//
// Its favicon is kept encoded: it is large and only passed on, and scanning it
// at each ping for characters to escape would cost more than writing the rest
// of the status.
func Parse(data []byte) (*Status, error) {
	value, err := ParseValue(data)
	if err != nil {
		return nil, err
	}
	fields, ok := value.(Object)
	if !ok {
		return nil, fmt.Errorf("must be a JSON object, got %s", quote(value))
	}
	return New(fields)
}

// New returns the status made of fields, after checking it, with its favicon
// encoded like Parse does, unless it already is a json.RawMessage.
func New(fields Object) (*Status, error) {
	if favicon, ok := fields.Get(faviconPath); ok {
		if _, encoded := favicon.(json.RawMessage); !encoded {
			// With cannot fail on a path without arrays.
			fields, _ = fields.With(faviconPath, json.RawMessage(AppendValue(nil, favicon)))
		}
	}
	s := &Status{Object: fields}
	if err := s.Check(); err != nil {
		return nil, err
	}
	return s, nil
}

type FieldError struct {
	Path Path
	Err  error
}

func (e *FieldError) Error() string {
	return e.Path.String() + ": " + e.Err.Error()
}

func (e *FieldError) Unwrap() error {
	return e.Err
}

// Check checks the types of the fields that this server reads: version.name,
// version.protocol, players.max and players.online, and the objects that hold
// them. Its errors are FieldErrors.
func (s *Status) Check() error {
	for _, path := range []Path{versionPath, playersPath} {
		if value, ok := s.Get(path); ok {
			if _, ok := value.(Object); !ok {
				return &FieldError{path, fmt.Errorf("must be a JSON object, got %s", quote(value))}
			}
		}
	}
	if value, ok := s.Get(versionNamePath); ok {
		if _, ok := value.(string); !ok {
			return &FieldError{versionNamePath, fmt.Errorf("must be a string, got %s", quote(value))}
		}
	}
	for _, path := range []Path{versionProtocolPath, playersMaxPath, playersOnlinePath} {
		if value, ok := s.Get(path); ok {
			if _, ok := int32Value(value); !ok {
				return &FieldError{path, fmt.Errorf("must be an integer between %d and %d, got %s", math.MinInt32, math.MaxInt32, quote(value))}
			}
		}
	}
	return nil
}

func int32Value(v any) (int32, bool) {
	number, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(string(number), 10, 32)
	return int32(n), err == nil
}

// WithProtocolVersion returns a copy of s whose version reports
// protocolVersion. A version without a name gets an empty one, as clients
// ignore versions without a name. s must be valid (see Check).
func (s *Status) WithProtocolVersion(protocolVersion int32) *Status {
	// With cannot fail on a path without arrays, which the version of a valid
	// status is not.
	fields := s.Object
	if _, ok := fields.Get(versionNamePath); !ok {
		fields, _ = fields.With(versionNamePath, "")
	}
	fields, _ = fields.With(versionProtocolPath, json.Number(strconv.Itoa(int(protocolVersion))))
	return &Status{Object: fields}
}

type Version struct {
	Name     string
	Protocol int32
}

type Players struct {
	Max    int32
	Online int32
}

// Version returns the version of s. Its fields are zero if s does not have
// them, or if they are invalid (see Check).
func (s *Status) Version() Version {
	var v Version
	if name, ok := s.Get(versionNamePath); ok {
		v.Name, _ = name.(string)
	}
	v.Protocol, _ = s.ProtocolVersion()
	return v
}

// ProtocolVersion returns the protocol version of s, and whether it has a
// valid one.
func (s *Status) ProtocolVersion() (int32, bool) {
	protocol, ok := s.Get(versionProtocolPath)
	if !ok {
		return 0, false
	}
	return int32Value(protocol)
}

// ClientProtocol is when to report the protocol version of each client in
// place of the one of a status, so that clients see the server as compatible.
type ClientProtocol int

const (
	// ClientProtocolAuto keeps the protocol versions that no client has (see
	// firstClientProtocolVersion).
	ClientProtocolAuto ClientProtocol = iota
	ClientProtocolAlways
	ClientProtocolNever
)

// ParseClientProtocol returns the ClientProtocol named name: "auto", "always"
// or "never".
func ParseClientProtocol(name string) (ClientProtocol, bool) {
	switch name {
	case "auto":
		return ClientProtocolAuto, true
	case "always":
		return ClientProtocolAlways, true
	case "never":
		return ClientProtocolNever, true
	}
	return ClientProtocolAuto, false
}

// firstClientProtocolVersion is the protocol version of 1.7.2, the first
// release with the modern protocol. Lower ones make clients show the status as
// incompatible, with its version name in place of the player counts: a common
// way to show a message such as "§cMaintenance", with -1 or 1 in plugins.
const firstClientProtocolVersion = 4

// Replaces reports whether the protocol version of st is replaced by the one
// of each client.
func (c ClientProtocol) Replaces(st *Status) bool {
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

// Players returns the player counts of s. They are zero if s does not have
// them, or if they are invalid (see Check).
func (s *Status) Players() Players {
	var p Players
	if count, ok := s.Get(playersMaxPath); ok {
		p.Max, _ = int32Value(count)
	}
	if count, ok := s.Get(playersOnlinePath); ok {
		p.Online, _ = int32Value(count)
	}
	return p
}

// Description returns the MOTD of s, a JSON text component, or nil if it has
// none.
func (s *Status) Description() json.RawMessage {
	description, ok := s.Get(descriptionPath)
	if !ok {
		return nil
	}
	return AppendValue(nil, description)
}
