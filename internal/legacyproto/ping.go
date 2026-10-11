package legacyproto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	pingID          = 0xfe
	pingPayload     = 0x01
	pluginMessageID = 0xfa
)

// ping16Start is how 1.6 pings start, before their MC|PingHost plugin message.
// Older clients only send its first byte (Beta 1.8 to 1.3) or two (1.4 to 1.5).
var ping16Start = []byte{pingID, pingPayload, pluginMessageID}

const pingHostChannel = "MC|PingHost"

// pingHostFixedLength is the length of the data of MC|PingHost without the
// server address: the protocol version (1 byte), the length of the server
// address (2 bytes) and the port (4 bytes).
const pingHostFixedLength = 1 + 2 + 4

// pingResponsePrefix starts the responses to the pings of 1.4 and later
// clients, which tells them apart from the kicks of older servers.
const pingResponsePrefix = "§1"

var ErrInvalidPing = errors.New("invalid legacy ping")

// PingVersion tells which clients sent a ping, as each range of versions has
// its own format.
type PingVersion int

const (
	PingBeta PingVersion = iota + 1 // Beta 1.8 to 1.3
	Ping14                          // 1.4 to 1.5
	Ping16                          // 1.6
)

func (v PingVersion) String() string {
	switch v {
	case PingBeta:
		return "beta-1.3"
	case Ping14:
		return "1.4-1.5"
	case Ping16:
		return "1.6"
	default:
		return fmt.Sprintf("PingVersion(%d)", int(v))
	}
}

type Ping struct {
	Version         PingVersion
	ProtocolVersion int32
	// HasHost tells whether the request holds the following fields, which 1.6
	// clients send but some tools omit.
	HasHost       bool
	ServerAddress string
	ServerPort    uint16
}

// Incomplete reports whether the bytes buffered in r are only the start of
// what may be a longer legacy ping: FE, FE 01, or FE 01 FA without MC|PingHost
// yet. Clients write their ping in small pieces, so its end can arrive a round
// trip after its start: callers should then wait briefly for more data before
// calling ReadPing.
func Incomplete(r *bufio.Reader) bool {
	received, _ := r.Peek(r.Buffered())
	return len(received) > 0 && len(received) <= len(ping16Start) && bytes.HasPrefix(ping16Start, received)
}

// ReadPing reads a legacy ping if the client started with one. Otherwise, it
// reports false without consuming anything.
//
// The versions are told apart by the bytes received along with the first one,
// like vanilla servers do (see Incomplete). A modern handshake can also start
// with the ping ID, when its length is 254, but its third byte (its packet ID)
// is then 0.
func ReadPing(r *bufio.Reader) (Ping, bool, error) {
	if first, err := r.Peek(1); err != nil || first[0] != pingID {
		return Ping{}, false, nil
	}
	received, _ := r.Peek(r.Buffered())

	// The bytes received so far are checked one at a time, as each is only
	// there if the previous ones match.
	switch {
	case len(received) == 1: // FE: Beta 1.8 to 1.3.
		_, _ = r.Discard(1)
		return Ping{Version: PingBeta, ProtocolVersion: unknownProtocolVersion}, true, nil
	case received[1] != pingPayload:
		// Neither a ping nor a modern handshake, whose length would be too
		// large.
		return Ping{}, true, fmt.Errorf("%w: payload %#x", ErrInvalidPing, received[1])
	case len(received) == 2: // FE 01: 1.4 to 1.5.
		_, _ = r.Discard(2)
		return Ping{Version: Ping14, ProtocolVersion: unknownProtocolVersion}, true, nil
	case received[2] == 0: // FE 01 00: a modern handshake of 254 bytes.
		return Ping{}, false, nil
	case received[2] != pluginMessageID:
		return Ping{}, true, fmt.Errorf("%w: unexpected packet %#x", ErrInvalidPing, received[2])
	}

	// FE 01 FA: 1.6, possibly followed by MC|PingHost.
	_, _ = r.Discard(len(ping16Start))
	ping := Ping{Version: Ping16, ProtocolVersion: unknownProtocolVersion}
	if len(received) == len(ping16Start) {
		return ping, true, nil
	}
	if err := readPingHost(r, &ping); err != nil {
		return Ping{}, true, fmt.Errorf("%w: %w", ErrInvalidPing, err)
	}
	return ping, true, nil
}

// readPingHost reads the MC|PingHost plugin message of 1.6 clients: its
// channel, then the length of its data, the protocol version, the server
// address and the port.
func readPingHost(r *bufio.Reader, ping *Ping) error {
	channel, err := readString(r, len(pingHostChannel))
	if err != nil {
		return fmt.Errorf("plugin message channel: %w", err)
	}
	if channel != pingHostChannel {
		return fmt.Errorf("unexpected plugin message channel %q", channel)
	}
	var dataLength uint16
	if err := binary.Read(r, binary.BigEndian, &dataLength); err != nil {
		return noEOF(err)
	}
	protocolVersion, err := r.ReadByte()
	if err != nil {
		return noEOF(err)
	}
	address, err := readString(r, maxServerAddressLength)
	if err != nil {
		return fmt.Errorf("server address: %w", err)
	}
	port, err := readPort(r)
	if err != nil {
		return err
	}
	if int(dataLength) != pingHostFixedLength+2*utf16Length(address) {
		return fmt.Errorf("data length %d does not match its content", dataLength)
	}

	ping.HasHost = true
	ping.ProtocolVersion = int32(protocolVersion)
	ping.ServerAddress = address
	ping.ServerPort = port
	return nil
}

type Status struct {
	ProtocolVersion int32
	VersionName     string
	// MOTD is legacy text: one line, formatted with § codes.
	MOTD          string
	OnlinePlayers int32
	MaxPlayers    int32
}

// AppendPingResponse appends the response to a ping of the given version: a
// kick packet holding the status. To fit the length limit, the version name
// and the MOTD are shortened if needed, so that the other fields stay whole.
func AppendPingResponse(b []byte, version PingVersion, s Status) []byte {
	online := strconv.Itoa(int(s.OnlinePlayers))
	maxPlayers := strconv.Itoa(int(s.MaxPlayers))

	if version == PingBeta {
		// Fields are separated by §, so the MOTD cannot have formatting codes.
		tail := "§" + online + "§" + maxPlayers
		motd := shortenUTF16(stripFormatting(s.MOTD), maxKickLength-utf16Length(tail))
		return AppendKick(b, motd+tail)
	}
	head := pingResponsePrefix + "\x00" + strconv.Itoa(int(s.ProtocolVersion)) + "\x00"
	tail := "\x00" + online + "\x00" + maxPlayers
	room := maxKickLength - utf16Length(head) - len("\x00") - utf16Length(tail)
	// The version name, which clients show in place of the player counts of
	// incompatible servers (such as "§cMaintenance"), keeps up to half of the
	// room, or what the MOTD leaves.
	versionName := shortenUTF16(s.VersionName, max(room/2, room-utf16Length(s.MOTD)))
	motd := shortenUTF16(s.MOTD, room-utf16Length(versionName))
	return AppendKick(b, head+versionName+"\x00"+motd+tail)
}

// MOTDFromComponent converts a modern MOTD, a JSON text component, to the
// legacy text shown in legacy server lists: only its first line.
func MOTDFromComponent(component json.RawMessage) string {
	line, _, _ := strings.Cut(TextFromComponent(component), "\n")
	return line
}

func stripFormatting(s string) string {
	var b strings.Builder
	skip := false
	for _, r := range s {
		switch {
		case skip:
			skip = false
		case r == '§':
			skip = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
