package legacyproto

import (
	"bufio"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const handshakeID = 0x02

var ErrInvalidLogin = errors.New("invalid legacy login")

// Login is the handshake that starts the login of a legacy client.
type Login struct {
	ProtocolVersion int32
	Username        string
	// HasHost tells whether the following fields are set: clients older than
	// 1.3 may only send the username.
	HasHost       bool
	ServerAddress string
	ServerPort    uint16
}

// ReadLogin reads the handshake of a legacy client trying to join, if the
// client started with one. Otherwise, it reports false without consuming
// anything. A modern packet cannot start the same way: it would be too short.
//
// Since 1.3, the handshake holds the protocol version, the username, the
// server address and the port. Before, it holds a single string, such as
// "username;host:port". The byte after the packet ID tells them apart: it is
// the protocol version (39 for 1.3.1, or more) or the high byte of the string
// length (0).
func ReadLogin(r *bufio.Reader) (Login, bool, error) {
	if first, err := r.Peek(1); err != nil || first[0] != handshakeID {
		return Login{}, false, nil
	}
	_, _ = r.Discard(1)

	next, err := r.Peek(1)
	if err != nil {
		return Login{}, true, fmt.Errorf("%w: %w", ErrInvalidLogin, noEOF(err))
	}
	var login Login
	if next[0] == 0 {
		login, err = readBetaHandshake(r)
	} else {
		login, err = readHandshake13(r)
	}
	if err != nil {
		return Login{}, true, fmt.Errorf("%w: %w", ErrInvalidLogin, err)
	}
	return login, true, nil
}

func readHandshake13(r *bufio.Reader) (Login, error) {
	protocolVersion, _ := r.ReadByte() // Peeked by ReadLogin, so it cannot fail.
	username, err := readString(r, maxUsernameLength)
	if err != nil {
		return Login{}, fmt.Errorf("username: %w", err)
	}
	if len(username) > maxUsernameLength {
		return Login{}, fmt.Errorf("username of %d bytes", len(username))
	}
	address, err := readString(r, maxServerAddressLength)
	if err != nil {
		return Login{}, fmt.Errorf("server address: %w", err)
	}
	port, err := readPort(r)
	if err != nil {
		return Login{}, err
	}
	return Login{
		ProtocolVersion: int32(protocolVersion),
		Username:        username,
		HasHost:         true,
		ServerAddress:   address,
		ServerPort:      port,
	}, nil
}

// readBetaHandshake reads the handshake of clients older than 1.3: a single
// string, the username optionally followed by ";host:port".
func readBetaHandshake(r *bufio.Reader) (Login, error) {
	s, err := readString(r, maxUsernameLength+len(";:65535")+maxServerAddressLength)
	if err != nil {
		return Login{}, fmt.Errorf("username and address: %w", err)
	}
	username, address, hasHost := strings.Cut(s, ";")
	if len(username) > maxUsernameLength {
		return Login{}, fmt.Errorf("username of %d bytes", len(username))
	}
	if !hasHost {
		return Login{ProtocolVersion: unknownProtocolVersion, Username: username}, nil
	}

	i := strings.LastIndexByte(address, ':')
	if i < 0 {
		return Login{}, fmt.Errorf("invalid address %q", address)
	}
	host, portText := address[:i], address[i+1:]
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return Login{}, fmt.Errorf("invalid address %q", address)
	}
	return Login{
		ProtocolVersion: unknownProtocolVersion,
		Username:        username,
		HasHost:         true,
		ServerAddress:   host,
		ServerPort:      uint16(port),
	}, nil
}
