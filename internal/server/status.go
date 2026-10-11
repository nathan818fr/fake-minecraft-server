package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sync"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/legacyproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// legacyPing14ProtocolVersion is reported to 1.4 and 1.5 clients as their
// protocol version: their ping does not tell it, so only 1.5.2 clients see the
// server as compatible.
const legacyPing14ProtocolVersion = 61 // 1.5.2

// clientProtocolVersion returns the protocol version to report to the client
// of sess in place of the one of the status, or false if it does not show one.
func clientProtocolVersion(sess *session) (int32, bool) {
	switch ping := sess.legacyPing; {
	case ping == nil:
		return sess.handshake.ProtocolVersion, true
	case ping.HasHost:
		return ping.ProtocolVersion, true
	case ping.Version == legacyproto.Ping14:
		return legacyPing14ProtocolVersion, true
	default:
		return 0, false // Beta to 1.3 clients do not show protocol versions.
	}
}

// CheckStatusLength checks that st, the status of a server with clientProtocol,
// fits in a status response, even with the longest protocol version if it may
// be the one of each client, and with the signature if signed.
func CheckStatusLength(st *status.Status, clientProtocol ClientProtocol, signed bool) error {
	if clientProtocol.replaces(st) {
		st = st.WithProtocolVersion(math.MinInt32)
	}
	return checkStatusJSON(appendStatusJSON(nil, st, signed))
}

// CheckKickLength checks that kick fits in a disconnect packet, which clients
// would reject otherwise.
func CheckKickLength(kick json.RawMessage) error {
	if n := modernproto.UTF16Length(kick); n > modernproto.MaxDisconnectReasonLength {
		return fmt.Errorf("too long: %d characters, the limit is %d", n, modernproto.MaxDisconnectReasonLength)
	}
	return nil
}

// signatureMember identifies this software in the statuses that it answers,
// which clients ignore.
const signatureMember = `"fakeMinecraftServer":true`

var signaturePath = status.Path{"fakeMinecraftServer"}

// appendStatusJSON appends the JSON of st, with signatureMember if signed,
// unless st has its own member of that name.
func appendStatusJSON(b []byte, st *status.Status, signed bool) []byte {
	b = st.AppendJSON(b)
	if !signed {
		return b
	}
	if _, ok := st.Get(signaturePath); ok {
		return b
	}
	b = b[:len(b)-1] // Before the closing brace.
	if len(st.Object) > 0 {
		b = append(b, ',')
	}
	b = append(b, signatureMember...)
	return append(b, '}')
}

// checkStatusJSON checks that json fits in a status response, which clients
// would reject otherwise.
func checkStatusJSON(json []byte) error {
	// The limit is in UTF-16 code units, which are never more than bytes: they
	// are only counted for long statuses.
	if len(json) <= modernproto.MaxStatusResponseLength {
		return nil
	}
	if n := modernproto.UTF16Length(json); n > modernproto.MaxStatusResponseLength {
		return fmt.Errorf("too long: %d characters, the limit is %d (is the favicon too large?)", n, modernproto.MaxStatusResponseLength)
	}
	return nil
}

// statusBuffers holds the buffers that statuses are encoded in, only while
// they are written: connections waiting for their client to close would
// otherwise each keep one, as large as the favicon.
var statusBuffers = sync.Pool{New: func() any { return new([]byte) }}

// writeStatus sends st in a status response, with the signature if signed,
// unless it does not fit.
func writeStatus(conn net.Conn, st *status.Status, signed bool) error {
	buf := statusBuffers.Get().(*[]byte)
	defer statusBuffers.Put(buf)
	*buf = appendStatusJSON((*buf)[:0], st, signed)
	if err := checkStatusJSON(*buf); err != nil {
		return fmt.Errorf("writing status: %w", err)
	}
	jsonLength := int32(len(*buf))
	header := modernproto.AppendPacketHeader(nil, modernproto.StatusResponseID, modernproto.VarIntSize(jsonLength)+int(jsonLength))
	header = modernproto.AppendVarInt(header, jsonLength)

	packet := net.Buffers{header, *buf}
	_, err := packet.WriteTo(conn)
	return err
}

func legacyStatus(st *status.Status) legacyproto.Status {
	version, players := st.Version(), st.Players()
	return legacyproto.Status{
		ProtocolVersion: version.Protocol,
		VersionName:     version.Name,
		MOTD:            legacyproto.MOTDFromComponent(st.Description()),
		OnlinePlayers:   players.Online,
		MaxPlayers:      players.Max,
	}
}
