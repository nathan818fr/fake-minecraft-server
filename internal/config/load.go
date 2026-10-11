package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// envPrefix starts the name of every variable, so that unrelated ones, such as
// the ones Kubernetes sets for its services (SERVERS_PORT for a service named
// servers), are not read as settings.
const envPrefix = "FMS_"

const envConfigFile = envPrefix + "CONFIG_FILE"

// Load reads the settings of the configuration file that FMS_CONFIG_FILE
// points to, if set, then of the environment variables in environ (as
// "NAME=value"), which override them, and checks them. Every mistake is
// reported at once: the ones found when reading are named after the variable
// or the file, and the others after the key of their setting. The
// configuration is returned even with mistakes, so that its log settings, if
// valid, can apply to them.
func Load(environ []string) (Config, error) {
	env := make(map[string]string, len(environ))
	for _, item := range environ {
		// Empty variables are unset.
		if name, value, _ := strings.Cut(item, "="); value != "" {
			env[name] = value
		}
	}

	var s settings
	var errs []error
	if path := env[envConfigFile]; path != "" {
		for _, err := range readConfigFile(path, &s) {
			errs = append(errs, fmt.Errorf("%s: %w", envConfigFile, err))
		}
		s.resolvePaths(filepath.Dir(path))
	}
	s.addDefaultServer()
	for _, v := range variables(&s, env) {
		for _, err := range v.apply() {
			errs = append(errs, fmt.Errorf("%s: %w", v.name, err))
		}
	}

	cfg, checkErrs := s.check()
	return cfg, errors.Join(append(errs, checkErrs...)...)
}

func readConfigFile(path string, s *settings) []error {
	data, err := os.ReadFile(path)
	if err != nil {
		return []error{err}
	}
	return decodeYAML(data, s)
}

type variable struct {
	name string
	// apply parses the variable into the setting, which it leaves as is if the
	// variable is invalid.
	apply func() []error
}

// variables returns the variables of env that override the settings in s.
// Their names are envPrefix then the keys of the settings in uppercase, with
// underscores for dots: FMS_CONNECTIONS_MAX_PER_IP for connections.max_per_ip.
// Each server has one, named after its ID, which is merged into it:
// FMS_SERVERS_LOBBY for servers.lobby.
func variables(s *settings, env map[string]string) []variable {
	var vars []variable
	var walk func(v reflect.Value, prefix string)
	walk = func(v reflect.Value, prefix string) {
		for i := range v.NumField() {
			key := prefix + v.Type().Field(i).Tag.Get("yaml")
			name := envPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
			field := v.Field(i)
			// Structs that decode themselves, such as optional, are values.
			_, isValue := field.Addr().Interface().(yaml.Unmarshaler)
			switch {
			case field.Kind() == reflect.Struct && !isValue:
				walk(field, key+".")
			case field.Kind() == reflect.Map:
				for _, entryName := range slices.Sorted(maps.Keys(env)) {
					if id, ok := strings.CutPrefix(entryName, name+"_"); ok {
						id, text := strings.ToLower(id), env[entryName]
						vars = append(vars, variable{entryName, func() []error { return mergeEntry(field, id, text) }})
					}
				}
			default:
				if text, ok := env[name]; ok {
					vars = append(vars, variable{name, func() []error { return setValue(field, text) }})
				}
			}
		}
	}
	walk(reflect.ValueOf(s).Elem(), "")
	return vars
}

// setValue parses text into the setting held by field, which is left as is if
// text is invalid. Text settings take text as is, and listen addresses are
// comma-separated (see isYAMLList). Other lists, such as rules, are written in
// YAML (such as a flow sequence, or JSON), and other values are read as YAML
// scalars, so that they take the same values as in the configuration file.
func setValue(field reflect.Value, text string) []error {
	value := reflect.New(field.Type())
	switch v := value.Interface().(type) {
	case *string:
		*v = text
	case *[]listenSettings:
		if isYAMLList(text) {
			if errs := decodeYAML([]byte(text), v); errs != nil {
				return singleLine(errs, text)
			}
			break
		}
		for item := range strings.SplitSeq(text, ",") {
			if item = strings.TrimSpace(item); item != "" {
				*v = append(*v, listenSettings{Address: item})
			}
		}
		if len(*v) == 0 {
			return []error{fmt.Errorf("must list at least one value, got %q", text)}
		}
	default:
		if field.Kind() == reflect.Slice {
			if errs := decodeYAML([]byte(text), v); errs != nil {
				return singleLine(errs, text)
			}
			break
		}
		node := yaml.Node{Kind: yaml.ScalarNode, Value: text, Line: 1}
		if err := node.Decode(v); err != nil {
			return withoutLine(yamlErrors(err))
		}
	}
	field.Set(value.Elem())
	return nil
}

// isYAMLList reports whether text, listen addresses, is rather a list of
// listeners written in YAML, with their options. Both can start with "[", as
// IPv6 addresses do.
func isYAMLList(text string) bool {
	text = strings.TrimSpace(text)
	first, _, _ := strings.Cut(text, ",")
	_, _, err := net.SplitHostPort(strings.TrimSpace(first))
	return strings.HasPrefix(text, "[") && err != nil
}

// UnmarshalYAML decodes a listener written as its address alone, or as a
// mapping with its options.
func (l *listenSettings) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&l.Address)
	}
	// Unlike the decoder of the file, Node.Decode accepts unknown keys.
	if err := checkKeys(node, reflect.TypeFor[listenSettings]()); err != nil {
		return err
	}
	type plain listenSettings // Without this method.
	return node.Decode((*plain)(l))
}

// checkKeys checks that the keys of node, if a mapping, are the ones of the
// fields of t, a struct. Its errors are worded like the reworded ones of the
// YAML library (see rewordYAMLError).
func checkKeys(node *yaml.Node, t reflect.Type) error {
	if node.Kind != yaml.MappingNode {
		return nil // Reported when decoded.
	}
	var errs []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key := node.Content[i]; !hasField(t, key.Value) {
			errs = append(errs, fmt.Sprintf("line %d: unknown key %s", key.Line, key.Value))
		}
	}
	if errs != nil {
		// The YAML library collects the errors of this type, and stops at the
		// others.
		return &yaml.TypeError{Errors: errs}
	}
	return nil
}

// hasField reports whether t, a struct, has a field with the YAML key key.
func hasField(t reflect.Type, key string) bool {
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("yaml") == key {
			return true
		}
	}
	return false
}

// mergeEntry merges text, written in YAML (such as a flow mapping, or JSON),
// into the entry with ID id of the servers held by field.
func mergeEntry(field reflect.Value, id, text string) []error {
	switch entries := field.Addr().Interface().(type) {
	case *map[string]serverSettings:
		return mergeServer(entries, id, text)
	default:
		panic(fmt.Sprintf("unsupported entries type %T", entries))
	}
}

// mergeServer merges text into the server with ID id of servers: the options
// it sets replace the ones of the server, and its status is merged as a JSON
// merge patch (RFC 7396).
func mergeServer(servers *map[string]serverSettings, id, text string) []error {
	current := (*servers)[id]
	merged := current
	merged.Status = nil // Decoded on its own, then merged as a patch.
	if errs := decodeYAML([]byte(text), &merged); errs != nil {
		return singleLine(errs, text)
	}
	if merged.Status != nil {
		merged.Status = mergePatch(current.Status, merged.Status)
	} else {
		merged.Status = current.Status
	}
	if *servers == nil {
		*servers = make(map[string]serverSettings)
	}
	(*servers)[id] = merged
	return nil
}

// singleLine removes the line numbers from errs if text, which they are about,
// is a single line, which needs none.
func singleLine(errs []error, text string) []error {
	if strings.Contains(text, "\n") {
		return errs
	}
	return withoutLine(errs)
}

// withoutLine removes the line numbers from errs, about a single line, which
// needs none.
func withoutLine(errs []error) []error {
	for i, err := range errs {
		errs[i] = errors.New(strings.TrimPrefix(err.Error(), "line 1: "))
	}
	return errs
}

// mergePatch returns target with patch merged into it: mappings are merged,
// null removes a field, and other values replace the ones of target, which is
// left as is.
func mergePatch(target, patch map[string]any) map[string]any {
	merged := maps.Clone(target)
	if merged == nil {
		merged = make(map[string]any, len(patch))
	}
	for name, value := range patch {
		switch value := value.(type) {
		case nil:
			delete(merged, name)
		case map[string]any:
			current, _ := merged[name].(map[string]any)
			merged[name] = mergePatch(current, value)
		default:
			merged[name] = value
		}
	}
	return merged
}

// decodeYAML decodes the YAML document in data into v. Unknown keys are
// errors, so that typos do not go unnoticed. A document without content sets
// nothing.
func decodeYAML(data []byte, v any) []error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return yamlErrors(err)
	}
	// Decode stops after the first document, without reading what follows.
	// The library reports content left after it as a missing document start,
	// with a line counted from 0, and left out when 0.
	var next yaml.Node
	switch err := dec.Decode(&next); {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		line := 1
		if m := yamlErrorLine.FindStringSubmatch(err.Error()); m != nil {
			n, _ := strconv.Atoi(m[1])
			line = n + 1
		}
		return []error{fmt.Errorf("line %d: unexpected content after the value", line)}
	default:
		return []error{fmt.Errorf("line %d: only one YAML document is allowed", next.Line)}
	}
}

// yamlErrors returns the errors that the YAML library joined in err.
func yamlErrors(err error) []error {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return []error{errors.New(strings.TrimPrefix(err.Error(), "yaml: "))}
	}
	errs := make([]error, len(typeErr.Errors))
	for i, msg := range typeErr.Errors {
		errs[i] = errors.New(rewordYAMLError(msg))
	}
	return errs
}

var (
	yamlErrorLine    = regexp.MustCompile(`line (\d+): `)
	yamlTypeError    = regexp.MustCompile("^(line \\d+): cannot unmarshal (!!\\w+)(?: (`.*`))? into (.+)$")
	yamlUnknownField = regexp.MustCompile(`^(line \d+): field (.+) not found in type .+$`)
)

// rewordYAMLError rewords the type errors of the YAML library, which name the
// Go types of the settings, in the terms of users and like the errors of the
// variables. Other messages, such as the ones of checkKeys and optional, are
// already worded so.
func rewordYAMLError(msg string) string {
	if m := yamlUnknownField.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("%s: unknown key %s", m[1], m[2])
	}
	if m := yamlTypeError.FindStringSubmatch(msg); m != nil {
		got := m[3] // The value, possibly truncated, unless a list or mapping.
		switch m[2] {
		case "!!seq":
			got = "a list"
		case "!!map":
			got = "a mapping"
		}
		return fmt.Sprintf("%s: must be %s, got %s", m[1], expected(m[4]), got)
	}
	return msg
}

// settingsTypePrefix starts the names of the types of the settings, which are
// all structs, such as "config.connectionSettings".
var settingsTypePrefix = strings.TrimSuffix(reflect.TypeFor[settings]().String(), "settings")

// expected describes the values of the Go type of a setting.
func expected(goType string) string {
	switch {
	case goType == "int", goType == "int32":
		return "an integer"
	case goType == "bool":
		return "true or false"
	case goType == "time.Duration":
		return `a duration such as "3s" or "500ms"`
	case goType == "string":
		return "text"
	case strings.HasPrefix(goType, "[]"):
		return "a list"
	case strings.HasPrefix(goType, "map["), strings.HasPrefix(goType, settingsTypePrefix):
		return "a mapping"
	default:
		return goType
	}
}
