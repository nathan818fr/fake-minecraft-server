package script

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	startime "go.starlark.net/lib/time"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// maxFormatDigits bounds the decimals and the integer digits of
// fms.format_number, as step limits do not bound the size of texts.
const maxFormatDigits = 20

// formatNumberBuiltin implements fms.format_number(n, sep="", point=".",
// decimals=0, digits=1): n with sep between groups of three integer digits,
// point before decimals digits, rounded half to even, and at least digits
// integer digits, padded with zeros. Starlark's own formatting has neither
// grouping, rounding nor padding.
func formatNumberBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var n starlark.Value
	sep, point := "", "."
	decimals, digits := 0, 1
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "n", &n, "sep?", &sep, "point?", &point, "decimals?", &decimals, "digits?", &digits); err != nil {
		return nil, err
	}
	if decimals < 0 || decimals > maxFormatDigits {
		return nil, fmt.Errorf("%s: decimals must be from 0 to %d, got %d", b.Name(), maxFormatDigits, decimals)
	}
	if digits < 1 || digits > maxFormatDigits {
		return nil, fmt.Errorf("%s: digits must be from 1 to %d, got %d", b.Name(), maxFormatDigits, digits)
	}
	var text string
	switch n := n.(type) {
	case starlark.Int:
		text = n.String()
		if decimals > 0 {
			text += "." + strings.Repeat("0", decimals)
		}
	case starlark.Float:
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return nil, fmt.Errorf("%s: cannot format %s", b.Name(), n)
		}
		text = strconv.FormatFloat(float64(n), 'f', decimals, 64)
	default:
		return nil, fmt.Errorf("%s: got %s, want int or float", b.Name(), n.Type())
	}

	text, negative := strings.CutPrefix(text, "-")
	integer, fraction, _ := strings.Cut(text, ".")
	if len(integer) < digits {
		integer = strings.Repeat("0", digits-len(integer)) + integer
	}
	var out strings.Builder
	// Rounded to zero, a negative number has no sign.
	if negative && strings.Trim(integer+fraction, "0") != "" {
		out.WriteByte('-')
	}
	for i := range len(integer) {
		if i > 0 && (len(integer)-i)%3 == 0 {
			out.WriteString(sep)
		}
		out.WriteByte(integer[i])
	}
	if fraction != "" {
		out.WriteString(point + fraction)
	}
	return starlark.String(out.String()), nil
}

// splitDurationBuiltin implements fms.split_duration(d), the days, hours,
// minutes and seconds of d, a duration or a number of seconds, truncated toward
// zero, so that each part has the sign of d, like Go durations.
func splitDurationBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var d starlark.Value
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &d); err != nil {
		return nil, err
	}
	var seconds int64
	switch d := d.(type) {
	case startime.Duration:
		seconds = int64(time.Duration(d) / time.Second)
	case starlark.Int:
		var ok bool
		if seconds, ok = d.Int64(); !ok {
			return nil, fmt.Errorf("%s: %s is out of range", b.Name(), d)
		}
	case starlark.Float:
		f := math.Trunc(float64(d))
		if math.IsNaN(f) || f < math.MinInt64 || f >= math.MaxInt64 {
			return nil, fmt.Errorf("%s: %s is out of range", b.Name(), d)
		}
		seconds = int64(f)
	default:
		return nil, fmt.Errorf("%s: got %s, want a duration or a number of seconds", b.Name(), d.Type())
	}
	return starlarkstruct.FromStringDict(starlark.String("duration_parts"), starlark.StringDict{
		"days":    starlark.MakeInt64(seconds / 86400),
		"hours":   starlark.MakeInt64(seconds % 86400 / 3600),
		"minutes": starlark.MakeInt64(seconds % 3600 / 60),
		"seconds": starlark.MakeInt64(seconds % 60),
	}), nil
}

// timeFunctions are the functions of go.starlark.net's time module that fms has
// as they are, besides from_timestamp (see fromTimestampBuiltin): all of them
// but now, which ctx.now replaces, as it is the only one that is not pure.
var timeFunctions = []string{"is_valid_timezone", "parse_duration", "parse_time", "time"}

// newTimeBuiltin returns the function name of go.starlark.net's time module as
// an fms function.
func newTimeBuiltin(name string) *starlark.Builtin {
	fn := startime.Module.Members[name].(*starlark.Builtin)
	return starlark.NewBuiltin("fms."+name, func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		v, err := fn.CallInternal(thread, args, kwargs)
		if err != nil {
			// Some of its errors start with its name, without fms.
			return nil, fmt.Errorf("%s: %s", b.Name(), strings.TrimPrefix(err.Error(), name+": "))
		}
		return v, nil
	})
}

var localFromTimestamp = newTimeBuiltin("from_timestamp")

// fromTimestampBuiltin implements fms.from_timestamp, in UTC, like ctx.now(),
// rather than in the local time of time.Unix.
func fromTimestampBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	v, err := localFromTimestamp.CallInternal(thread, args, kwargs)
	if err != nil {
		return nil, err
	}
	return startime.Time(time.Time(v.(startime.Time)).UTC()), nil
}
