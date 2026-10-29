package config

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

const schemaPath = "../../config.schema.json"

// schemaNode is a schema as decoded from JSON.
type schemaNode = map[string]any

func readSchema(t *testing.T) schemaNode {
	t.Helper()
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var root schemaNode
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaPath, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// resolve returns node with the definition it refers to, if any, merged under
// its own keywords.
func resolve(root, node schemaNode) schemaNode {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	name, ok := strings.CutPrefix(ref, "#/$defs/")
	if !ok {
		panic("unsupported $ref " + ref)
	}
	merged := resolve(root, root["$defs"].(schemaNode)[name].(schemaNode))
	merged = maps.Clone(merged)
	for k, v := range node {
		if k != "$ref" {
			merged[k] = v
		}
	}
	return merged
}

// schemaTypes returns the JSON types that node accepts, from its type, enum
// and oneOf.
func schemaTypes(root, node schemaNode) []string {
	node = resolve(root, node)
	var types []string
	switch typ := node["type"].(type) {
	case string:
		types = append(types, typ)
	case []any:
		for _, t := range typ {
			types = append(types, t.(string))
		}
	}
	for _, v := range asSlice(node["enum"]) {
		switch v.(type) {
		case string:
			types = append(types, "string")
		case bool:
			types = append(types, "boolean")
		case float64:
			types = append(types, "integer")
		}
	}
	for _, branch := range asSlice(node["oneOf"]) {
		types = append(types, schemaTypes(root, branch.(schemaNode))...)
	}
	slices.Sort(types)
	return slices.Compact(types)
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// objectBranch returns the branch of node that is an object, such as the
// mapping form of a listener, which can also be an address alone.
func objectBranch(root, node schemaNode) schemaNode {
	node = resolve(root, node)
	for _, branch := range asSlice(node["oneOf"]) {
		if b := resolve(root, branch.(schemaNode)); slices.Contains(schemaTypes(root, b), "object") {
			return b
		}
	}
	return node
}

// checkSchema checks that node accepts the values of typ, the Go type of the
// setting at key, as the YAML decoder reads them: the same keys for the
// structs, and the same kinds of values.
func checkSchema(t *testing.T, root, node schemaNode, typ reflect.Type, key string) {
	t.Helper()
	if strings.HasPrefix(typ.Name(), "optional[") {
		typ = typ.Field(0).Type
	}
	var want []string
	switch {
	case typ == reflect.TypeFor[time.Duration]():
		if node["$ref"] != "#/$defs/duration" {
			t.Errorf("%s: a duration, but not #/$defs/duration", key)
		}
		want = []string{"string"}
	case typ == reflect.TypeFor[listenSettings]():
		want = []string{"object", "string"}
	case typ == reflect.TypeFor[statusSettings]():
		want = []string{"object"}
	case typ.Kind() == reflect.Interface: // The kick message: text or a component.
		want = []string{"array", "object", "string"}
	case typ.Kind() == reflect.String:
		want = []string{"string"}
	case typ.Kind() == reflect.Bool:
		want = []string{"boolean"}
	case typ.Kind() == reflect.Int, typ.Kind() == reflect.Int32:
		want = []string{"integer"}
	case typ.Kind() == reflect.Slice:
		want = []string{"array"}
	case key == "": // A document without content sets nothing.
		want = []string{"null", "object"}
	case typ.Kind() == reflect.Map, typ.Kind() == reflect.Struct:
		want = []string{"object"}
	default:
		t.Fatalf("%s: unsupported type %s", key, typ)
	}
	if got := schemaTypes(root, node); !slices.Equal(got, want) {
		t.Errorf("%s: types %v, want %v (for %s)", key, got, want, typ)
	}

	switch {
	case typ == reflect.TypeFor[statusSettings]():
		// Its fields are sent as is, the ones of mods included.
	case typ.Kind() == reflect.Slice:
		checkSchema(t, root, resolve(root, node)["items"].(schemaNode), typ.Elem(), key+".*")
	case typ.Kind() == reflect.Map:
		checkSchema(t, root, resolve(root, node)["additionalProperties"].(schemaNode), typ.Elem(), key+".*")
	case typ.Kind() == reflect.Struct:
		object := objectBranch(root, node)
		if object["additionalProperties"] != false {
			t.Errorf("%s: unknown keys must be rejected, as the configuration file rejects them", key)
		}
		properties, _ := object["properties"].(schemaNode)
		var keys []string
		for i := range typ.NumField() {
			field := typ.Field(i)
			name := field.Tag.Get("yaml")
			keys = append(keys, name)
			property, ok := properties[name].(schemaNode)
			if !ok {
				t.Errorf("%s: missing from the schema", strings.TrimPrefix(key+"."+name, "."))
				continue
			}
			checkSchema(t, root, property, field.Type, strings.TrimPrefix(key+"."+name, "."))
		}
		for name := range properties {
			if !slices.Contains(keys, name) {
				t.Errorf("%s: in the schema, but not a setting", strings.TrimPrefix(key+"."+name, "."))
			}
		}
	}
}

// The schema has the keys of the settings, with their types, so that editors
// neither miss a setting nor accept one that the server does not know.
func TestSchemaSettings(t *testing.T) {
	root := readSchema(t)
	checkSchema(t, root, root, reflect.TypeFor[settings](), "")
}

// schemaDefaults returns the defaults in node, by key, "*" standing for any
// entry of a list or mapping.
func schemaDefaults(root, node schemaNode, key string, defaults map[string]any) {
	node = resolve(root, node)
	if def, ok := node["default"]; ok {
		defaults[key] = def
	}
	prefix := key + "."
	if key == "" {
		prefix = ""
	}
	for _, branch := range asSlice(node["oneOf"]) {
		schemaDefaults(root, branch.(schemaNode), key, defaults)
	}
	if items, ok := node["items"].(schemaNode); ok {
		schemaDefaults(root, items, prefix+"*", defaults)
	}
	if entries, ok := node["additionalProperties"].(schemaNode); ok {
		schemaDefaults(root, entries, prefix+"*", defaults)
	}
	properties, _ := node["properties"].(schemaNode)
	for name, property := range properties {
		schemaDefaults(root, property.(schemaNode), prefix+name, defaults)
	}
}

// The defaults that editors show are the ones of the server.
func TestSchemaDefaults(t *testing.T) {
	root := readSchema(t)
	defaults := map[string]any{}
	schemaDefaults(root, root, "", defaults)
	text := func(key string) string {
		s, ok := defaults[key].(string)
		if !ok {
			t.Fatalf("%s: default %v, want text", key, defaults[key])
		}
		return s
	}
	duration := func(key string) time.Duration {
		d, err := time.ParseDuration(text(key))
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return d
	}
	number := func(key string) int {
		n, ok := defaults[key].(float64)
		if !ok {
			t.Fatalf("%s: default %v, want a number", key, defaults[key])
		}
		return int(n)
	}
	var c checker
	logFormat, logLevel := c.logFormat(text("log.format")), c.logLevel(text("log.level"))
	clientProtocol, _ := status.ParseClientProtocol(text("servers.*.client_protocol"))
	checks := map[string]bool{
		"listen":                         reflect.DeepEqual(defaults["listen"], []any{":" + strconv.Itoa(modernproto.DefaultPort)}),
		"listen.*.proxy_protocol":        defaults["listen.*.proxy_protocol"] == false,
		"connections.timeout":            duration("connections.timeout") == defaultConnectionTimeout,
		"connections.handshake_timeout":  duration("connections.handshake_timeout") == defaultHandshakeTimeout,
		"connections.max":                number("connections.max") == defaultMaxConnections,
		"connections.max_per_ip":         number("connections.max_per_ip") == defaultMaxConnectionsPerIP,
		"connections.rate_limit_per_ip":  number("connections.rate_limit_per_ip") == defaultRateLimitPerIP,
		"log.format":                     logFormat == defaultLogFormat,
		"log.level":                      logLevel == defaultLogLevel,
		"log.rate_limit":                 number("log.rate_limit") == defaultLogRateLimit,
		"no_software_signature":          defaults["no_software_signature"] == false,
		"servers.*.client_protocol":      clientProtocol == status.ClientProtocolAuto,
		"servers.*.kick":                 text("servers.*.kick") == defaultKickMessage,
		"upstreams.*.interval":           duration("upstreams.*.interval") == defaultUpstreamInterval,
		"upstreams.*.handshake.protocol": number("upstreams.*.handshake.protocol") == defaultUpstreamProtocolVersion,
	}
	if c.errs != nil {
		t.Errorf("the defaults of the schema are invalid: %v", c.errs)
	}
	for key, ok := range checks {
		if !ok {
			t.Errorf("%s: default %v differs from the server's", key, defaults[key])
		}
	}
	for key := range defaults {
		if _, ok := checks[key]; !ok {
			t.Errorf("%s: default %v not checked against the server's", key, defaults[key])
		}
	}
}

// yamlBlock finds the YAML code blocks of Markdown, possibly indented, as in
// lists.
var yamlBlock = regexp.MustCompile("(?ms)^([ \t]*)```yaml\n(.*?)\n[ \t]*```$")

// documentedConfigurations returns the configurations of the examples and of
// the YAML blocks of the documentation, by name.
func documentedConfigurations(t *testing.T) map[string][]byte {
	t.Helper()
	configs := map[string][]byte{}
	examples, _ := filepath.Glob("../../examples/*.yaml")
	for _, path := range examples {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		configs[filepath.Base(path)] = data
	}
	docs, _ := filepath.Glob("../../docs/*.md")
	for _, path := range append(docs, "../../README.md") {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range yamlBlock.FindAllSubmatch(data, -1) {
			indent := string(m[1])
			var block strings.Builder
			for line := range strings.SplitSeq(string(m[2]), "\n") {
				block.WriteString(strings.TrimPrefix(line, indent) + "\n")
			}
			configs[filepath.Base(path)+" block "+strconv.Itoa(i+1)] = []byte(block.String())
		}
	}
	if len(examples) == 0 || len(docs) == 0 {
		t.Fatal("no examples or documentation found")
	}
	return configs
}

// validate validates the YAML document data against sch.
func validate(sch *jsonschema.Schema, data []byte) error {
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return err
	}
	text, err := json.Marshal(value)
	if err != nil {
		return err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(text))
	if err != nil {
		return err
	}
	return sch.Validate(instance)
}

// The schema accepts every configuration of the examples and the documentation
// that the configuration file reads, so that editors show no false errors.
// Blocks that the file does not read, such as the fragments of a setting, are
// skipped.
func TestSchemaAcceptsDocumentedConfigurations(t *testing.T) {
	sch := compileSchema(t)
	validated := 0
	for name, data := range documentedConfigurations(t) {
		var s settings
		if decodeYAML(data, &s) != nil {
			continue
		}
		validated++
		if err := validate(sch, data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if validated < 10 {
		t.Errorf("only %d configurations validated", validated)
	}
}

// The schema rejects the mistakes that editors should show.
func TestSchemaRejects(t *testing.T) {
	sch := compileSchema(t)
	for name, text := range map[string]string{
		"unknown key":                 "conections: { max: 5 }",
		"unknown nested key":          "connections: { maximum: 5 }",
		"duration without unit":       "connections: { timeout: 3 }",
		"negative count":              "connections: { max: -1 }",
		"unknown log level":           "log: { level: verbose }",
		"server ID":                   "servers: { Lobby: {} }",
		"unknown client protocol":     "servers: { default: { client_protocol: sometimes } }",
		"version name as a number":    "servers: { default: { status: { version: { name: 1.20 } } } }",
		"upstream without address":    "upstreams: { lobby: { interval: 30s } }",
		"handshake port":              "upstreams: { lobby: { address: a.com, handshake: { port: 70000 } } }",
		"rule without server":         "rules: [{ host: [a.com] }]",
		"rule named default":          "rules: [{ name: default, server: default }]",
		"rule named by digits":        "rules: [{ name: '12', server: default }]",
		"script and script file":      "script: X = 1\nscript_file: x.star",
		"listen address without port": "listen: [localhost]",
		"trusted proxies alone":       "listen: [{ address: ':25565', trusted_proxies: [10.0.0.0/8] }]",
	} {
		if err := validate(sch, []byte(text)); err == nil {
			t.Errorf("%s: %q accepted", name, text)
		}
	}
}
