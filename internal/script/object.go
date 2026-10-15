package script

import (
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/starlark"
)

// object is a value of scripts made of attributes, such as client, ctx or
// server: its kind tells its attributes, which are computed from its state
// when read, so that objects cost nothing for the attributes that scripts do
// not read. Objects are read-only.
type object[S any] struct {
	kind  *objectKind[S]
	state S
	// methods are its methods, by objectAttr.methodIndex, made when first
	// read: a pointer, so that the objects of kinds without methods stay small.
	methods *[maxObjectMethods]*starlark.Builtin
	// read has a bit for each attribute read, by index in kind.attrs, String
	// included, to tell which values a script failed with (see clientFields).
	read uint32
}

// objectKind is a kind of object: its name, which type() tells, and its
// attributes. str, if set, writes its objects, and otherwise they are written
// with all their fields, such as client(host = "a.com", port = 25565).
type objectKind[S any] struct {
	name  string
	attrs []objectAttr[S]
	names []string
	str   func(state S) string
}

// objectAttr is an attribute of the objects of a kind: a field, whose value is
// computed from their state, or a method.
type objectAttr[S any] struct {
	name   string
	field  func(state S) starlark.Value
	method func(state S, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error)
	// methodIndex is the index of the method in object.methods.
	methodIndex int
}

// maxObjectAttrs is the number of bits of object.read, and maxObjectMethods
// the size of object.methods.
const (
	maxObjectAttrs   = 32
	maxObjectMethods = 8
)

// newObjectKind returns the kind of object named name with the attributes
// attrs, written by str if not nil.
func newObjectKind[S any](name string, str func(state S) string, attrs []objectAttr[S]) *objectKind[S] {
	if len(attrs) > maxObjectAttrs {
		panic("script: too many attributes for " + name)
	}
	k := &objectKind[S]{name: name, attrs: attrs, str: str}
	methods := 0
	for i, a := range attrs {
		k.names = append(k.names, a.name)
		if a.method != nil {
			attrs[i].methodIndex = methods
			methods++
		}
	}
	if methods > maxObjectMethods {
		panic("script: too many methods for " + name)
	}
	return k
}

// new returns an object of the kind k with state.
func (k *objectKind[S]) new(state S) *object[S] {
	return &object[S]{kind: k, state: state}
}

var _ starlark.HasAttrs = (*object[*Client])(nil)

func (o *object[S]) String() string {
	if o.kind.str != nil {
		return o.kind.str(o.state)
	}
	var b strings.Builder
	b.WriteString(o.kind.name + "(")
	sep := ""
	for i, a := range o.kind.attrs {
		if a.field != nil {
			o.read |= 1 << i
			b.WriteString(sep + a.name + " = " + a.field(o.state).String())
			sep = ", "
		}
	}
	b.WriteString(")")
	return b.String()
}

func (o *object[S]) Type() string          { return o.kind.name }
func (o *object[S]) Freeze()               {}
func (o *object[S]) Truth() starlark.Bool  { return true }
func (o *object[S]) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: %s", o.kind.name) }

func (o *object[S]) Attr(name string) (starlark.Value, error) {
	i := slices.Index(o.kind.names, name)
	if i < 0 {
		return nil, nil // Starlark reports it, with a spelling hint.
	}
	o.read |= 1 << i
	a := &o.kind.attrs[i]
	if a.field != nil {
		return a.field(o.state), nil
	}
	if o.methods == nil {
		o.methods = new([maxObjectMethods]*starlark.Builtin)
	}
	m := &o.methods[a.methodIndex]
	if *m == nil {
		// The state, not o: o would escape to the heap whenever read.
		method, state := a.method, o.state
		*m = starlark.NewBuiltin(o.kind.name+"."+name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return method(state, b, args, kwargs)
		})
	}
	return *m, nil
}

// AttrNames returns a copy, which callers may sort, as dir does.
func (o *object[S]) AttrNames() []string { return slices.Clone(o.kind.names) }
