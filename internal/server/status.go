package server

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/legacyproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
)

func encodeStatus(cfg Config) (head, tail []byte, err error) {
	name, err := json.Marshal(cfg.VersionName)
	if err != nil {
		return nil, nil, err
	}
	rest, err := json.Marshal(struct {
		Players     statusPlayers   `json:"players"`
		Description json.RawMessage `json:"description"`
		Favicon     string          `json:"favicon,omitempty"`
		// Signature identifies this software, which clients ignore.
		Signature bool `json:"fakeMinecraftServer"`
	}{
		Players:     statusPlayers{Max: cfg.MaxPlayers, Online: cfg.OnlinePlayers, Sample: []struct{}{}},
		Description: cfg.MOTD,
		Favicon:     cfg.Favicon,
		Signature:   true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encoding status: %w", err)
	}

	// The version comes first, so that the rest of the status, without its
	// opening brace, can follow the protocol version written in between.
	head = append([]byte(`{"version":{"name":`), name...)
	head = append(head, `,"protocol":`...)
	tail = append([]byte(`},`), rest[1:]...)
	return head, tail, nil
}

type statusPlayers struct {
	Max    int32      `json:"max"`
	Online int32      `json:"online"`
	Sample []struct{} `json:"sample"`
}

// writeStatus sends the status response with a single vectored write, so that
// its bulk (the favicon) is never copied.
func (s *Server) writeStatus(conn net.Conn, clientProtocolVersion int32) error {
	protocolVersion := s.cfg.ProtocolVersion
	if s.cfg.MirrorProtocolVersion {
		protocolVersion = clientProtocolVersion
	}
	protocolVersionText := strconv.AppendInt(nil, int64(protocolVersion), 10)

	jsonLength := int32(len(s.statusHead) + len(protocolVersionText) + len(s.statusTail))
	header := modernproto.AppendPacketHeader(nil, modernproto.StatusResponseID, modernproto.VarIntSize(jsonLength)+int(jsonLength))
	header = modernproto.AppendVarInt(header, jsonLength)

	packet := net.Buffers{header, s.statusHead, protocolVersionText, s.statusTail}
	_, err := packet.WriteTo(conn)
	return err
}

// legacyPing14ProtocolVersion is the legacy protocol version reported to 1.4
// and 1.5 clients when mirroring protocol versions: their ping does not tell
// theirs, so only 1.5.2 clients see the server as compatible.
const legacyPing14ProtocolVersion = 61 // 1.5.2

func (s *Server) legacyStatus(ping legacyproto.Ping) legacyproto.Status {
	protocolVersion := s.cfg.ProtocolVersion
	if s.cfg.MirrorProtocolVersion {
		switch {
		case ping.HasHost:
			protocolVersion = ping.ProtocolVersion
		case ping.Version == legacyproto.Ping14:
			protocolVersion = legacyPing14ProtocolVersion
		}
	}
	return legacyproto.Status{
		ProtocolVersion: protocolVersion,
		VersionName:     s.cfg.VersionName,
		MOTD:            s.legacyMOTD,
		OnlinePlayers:   s.cfg.OnlinePlayers,
		MaxPlayers:      s.cfg.MaxPlayers,
	}
}
