package modernproto

import (
	"encoding/hex"
	"fmt"
	"io"
	"math"
)

// maxUsernameBytes limits the username to 16 bytes. Vanilla limits it to 16
// UTF-16 code units, but only accepts ASCII letters, digits and underscores in
// usernames: counting bytes is simpler, and only rejects invalid usernames.
const maxUsernameBytes = 16

// snapshotProtocolVersionBase is added to the protocol versions of snapshots,
// since 1.16.4, which sets them apart from those of releases.
const snapshotProtocolVersionBase = 0x40000000

// Protocol versions from which the Login Start packet has an optional player
// UUID, then a mandatory one.
const (
	optionalUUIDProtocolVersion          = 761                               // 1.19.3
	mandatoryUUIDProtocolVersion         = 764                               // 1.20.2
	mandatoryUUIDSnapshotProtocolVersion = snapshotProtocolVersionBase + 153 // 1.20.2-rc2
)

const uuidLength = 16

type LoginStart struct {
	Username string
	// UUID is formatted like "069a79f4-44e9-4726-a5be-fca90e38aaf5", or empty
	// if the client did not send one.
	UUID string
}

// ReadLoginStart reads the beginning of the Login Start packet, which clients
// send right after a login handshake: the username, then the player UUID
// for the protocol versions that have one at that position. The rest of the
// packet is left unread.
//
// A missing or truncated UUID is not an error: the login is returned without
// it, as it is only informative.
func ReadLoginStart(r Reader, protocolVersion int32) (LoginStart, error) {
	// Only the beginning of the packet is read, so its length is not limited.
	remaining, err := readPacketHeader(r, LoginStartID, 0, math.MaxInt)
	if err != nil {
		return LoginStart{}, err
	}

	username, err := readString(r, maxUsernameBytes)
	if err != nil {
		return LoginStart{}, fmt.Errorf("username: %w", err)
	}
	remaining -= VarIntSize(int32(len(username))) + len(username)
	if remaining < 0 {
		return LoginStart{}, fmt.Errorf("%w: username exceeds the packet", ErrInvalidPacketLength)
	}
	login := LoginStart{Username: username}

	switch loginStartUUIDField(protocolVersion) {
	case uuidNone:
		return login, nil
	case uuidOptional:
		if remaining < 1 {
			return login, nil
		}
		present, err := r.ReadByte()
		if err != nil || present != 1 {
			return login, nil
		}
		remaining--
	}
	if remaining < uuidLength {
		return login, nil
	}
	var uuid [uuidLength]byte
	if _, err := io.ReadFull(r, uuid[:]); err != nil {
		return login, nil
	}
	login.UUID = formatUUID(uuid)
	return login, nil
}

// uuidField is how the Login Start packet of a protocol version holds the
// player UUID, after the username.
type uuidField int

const (
	uuidNone      uuidField = iota // No UUID.
	uuidOptional                   // A boolean, then the UUID if it is true.
	uuidMandatory                  // The UUID.
)

// loginStartUUIDField returns how the Login Start packet of a protocol version
// holds the player UUID. Snapshots older than 1.20.2-rc2 are ignored.
func loginStartUUIDField(protocolVersion int32) uuidField {
	switch {
	case protocolVersion >= snapshotProtocolVersionBase:
		if protocolVersion >= mandatoryUUIDSnapshotProtocolVersion {
			return uuidMandatory
		}
		return uuidNone
	case protocolVersion >= mandatoryUUIDProtocolVersion:
		return uuidMandatory
	case protocolVersion >= optionalUUIDProtocolVersion:
		return uuidOptional
	default:
		return uuidNone
	}
}

func formatUUID(uuid [uuidLength]byte) string {
	var buf [36]byte
	hex.Encode(buf[0:8], uuid[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], uuid[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], uuid[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], uuid[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], uuid[10:16])
	return string(buf[:])
}
