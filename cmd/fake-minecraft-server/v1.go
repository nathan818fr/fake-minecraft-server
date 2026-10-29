package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

const upgradeGuide = "https://github.com/nathan818fr/fake-minecraft-server/blob/main/docs/upgrading-from-v1.md"

// v1Variables are in the order of the v1 README.
var v1Variables = []string{
	"LISTEN_HOST", "LISTEN_PORT", "LISTEN_BACKLOG", "KICK_MESSAGE", "MOTD",
	"FAVICON", "MAX_PLAYERS", "ONLINE_PLAYERS", "PROTOCOL_NAME", "PROTOCOL_VERSION",
}

type v1Server struct {
	Status struct {
		Description any `yaml:"description"`
		Version     struct {
			Name     string `yaml:"name"`
			Protocol int    `yaml:"protocol"`
		} `yaml:"version"`
		Players struct {
			Max    int `yaml:"max"`
			Online int `yaml:"online"`
		} `yaml:"players"`
		Favicon string `yaml:"favicon,omitempty"`
	} `yaml:"status"`
	Kick any `yaml:"kick,omitempty"`
}

type v1Setup struct {
	read []string
	// ignored tells that a v2 configuration is set, so that the v1 variables
	// do not apply.
	ignored bool
	// replacement holds the FMS_ variables, as "NAME=value".
	replacement []string
	// errs are about the values ignored, as v1 did.
	errs []error
}

// fromV1 returns environ with the FMS_ variables that replace its v1 ones, so
// that a v1 setup keeps working when its image tag, such as latest, moves to
// v2. With a v2 configuration (any FMS_ variable), environ is returned as is.
func fromV1(environ []string) ([]string, v1Setup) {
	env := make(map[string]string, len(environ))
	var v v1Setup
	for _, item := range environ {
		// Empty variables are unset, in v1 and v2.
		if name, value, _ := strings.Cut(item, "="); value != "" {
			env[name] = value
			v.ignored = v.ignored || strings.HasPrefix(name, "FMS_")
		}
	}
	for _, name := range v1Variables {
		if env[name] != "" {
			v.read = append(v.read, name)
		}
	}
	if len(v.read) == 0 || v.ignored {
		return environ, v
	}

	host, port := env["LISTEN_HOST"], env["LISTEN_PORT"]
	if host != "" || port != "" {
		n := v.integer(env, "LISTEN_PORT", modernproto.DefaultPort)
		if n < 1 || n > 65535 {
			v.errs = append(v.errs, fmt.Errorf("LISTEN_PORT: must be between 1 and 65535, got %q", port))
			n = modernproto.DefaultPort
		}
		v.replacement = append(v.replacement, "FMS_LISTEN="+net.JoinHostPort(host, strconv.Itoa(n)))
	}
	if env["LISTEN_BACKLOG"] != "" {
		v.errs = append(v.errs, errors.New("LISTEN_BACKLOG: has no replacement in v2"))
	}

	var s v1Server
	s.Status.Description = v1Text(cmp.Or(env["MOTD"], "§eHello World!"))
	s.Status.Version.Name = env["PROTOCOL_NAME"]
	s.Status.Version.Protocol = v.integer(env, "PROTOCOL_VERSION", 0)
	s.Status.Players.Max = v.integer(env, "MAX_PLAYERS", 0)
	s.Status.Players.Online = v.integer(env, "ONLINE_PLAYERS", 0)
	if favicon := env["FAVICON"]; favicon != "" {
		// v1 left out the favicons it could not read, and v2 stops on them.
		var err error
		if strings.HasPrefix(favicon, "data:") {
			err = status.CheckFavicon(favicon)
		} else {
			_, err = status.ReadFavicon(favicon)
		}
		if err != nil {
			v.errs = append(v.errs, fmt.Errorf("FAVICON: %w", err))
		} else {
			s.Status.Favicon = favicon
		}
	}
	if kick := env["KICK_MESSAGE"]; kick != "" {
		s.Kick = v1Text(kick)
	}
	// On one line, with text in double quotes, like the examples of the docs,
	// so that single shell quotes keep it as is.
	var node yaml.Node
	_ = node.Encode(s) // Text and numbers only.
	node.Style = yaml.FlowStyle
	doubleQuote(&node)
	text, _ := yaml.Marshal(&node)
	v.replacement = append(v.replacement, "FMS_SERVERS_DEFAULT="+strings.TrimSuffix(string(text), "\n"))

	return append(environ, v.replacement...), v
}

func doubleQuote(node *yaml.Node) {
	for i, child := range node.Content {
		if node.Kind == yaml.MappingNode && i%2 == 0 {
			continue // A key.
		}
		if child.Kind == yaml.ScalarNode && child.Tag == "!!str" {
			child.Style = yaml.DoubleQuotedStyle
		}
		doubleQuote(child)
	}
}

// integer returns the integer of the variable name, or def if unset or
// invalid, as in v1.
func (v *v1Setup) integer(env map[string]string, name string, def int) int {
	text := env[name]
	if text == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		v.errs = append(v.errs, fmt.Errorf("%s: must be an integer, got %q", name, text))
		return def
	}
	return n
}

// v1Text returns text as a text component: JSON if it starts with "{" and is
// valid, else plain text, as in v1 (v2 requires valid JSON).
func v1Text(text string) any {
	if strings.HasPrefix(text, "{") && !json.Valid([]byte(text)) {
		return map[string]string{"text": text}
	}
	return text
}

// report writes the replacement to w, as text to copy, but logs the ignored v1
// variables, as their v2 configuration may log in JSON.
func (v v1Setup) report(w io.Writer, logger *slog.Logger) {
	switch {
	case len(v.read) == 0:
	case v.ignored:
		logger.Warn("v1 variables ignored, as FMS_ variables are set", slog.String("v1", strings.Join(v.read, ",")), slog.String("help", upgradeGuide))
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "Warning: deprecated v1 variables: %s.\nThey still work. Replace them with:\n\n", strings.Join(v.read, ", "))
		for _, item := range v.replacement {
			name, value, _ := strings.Cut(item, "=")
			fmt.Fprintf(&b, "  %s=%s\n", name, shellQuote(value))
		}
		if v.errs != nil {
			b.WriteString("\nIgnored, as in v1:\n\n")
			for _, err := range v.errs {
				fmt.Fprintf(&b, "  %s\n", err)
			}
		}
		fmt.Fprintf(&b, "\nSee %s\n\n", upgradeGuide)
		_, _ = io.WriteString(w, b.String())
	}
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return !strings.ContainsRune("-_./:,@%+", r) && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
