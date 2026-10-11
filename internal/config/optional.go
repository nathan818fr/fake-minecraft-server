package config

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// optional is a setting whose zero value is valid but is not its default.
type optional[T any] struct {
	value T
	set   bool
}

func (o optional[T]) or(def T) T {
	if o.set {
		return o.value
	}
	return def
}

// UnmarshalYAML sets o only if node holds a valid value: the YAML library
// would leave a zero value behind, reported as a second mistake.
func (o *optional[T]) UnmarshalYAML(node *yaml.Node) error {
	var value T
	switch any(value).(type) {
	case int, int32:
		// The YAML library would truncate floats, 5.5 into 5. The error is
		// worded like its reworded ones (see rewordYAMLError), and a TypeError,
		// which it collects with the others.
		if node.ShortTag() == "!!float" {
			return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: must be an integer, got `%s`", node.Line, node.Value)}}
		}
	}
	if err := node.Decode(&value); err != nil {
		return err
	}
	*o = optional[T]{value, true}
	return nil
}
