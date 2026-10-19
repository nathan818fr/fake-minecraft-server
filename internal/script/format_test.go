package script

import (
	"testing"
	_ "time/tzdata" // Time zones, whatever the system has.

	"go.starlark.net/starlark"
)

// evalFms returns the value of expr, an expression that uses fms.
func evalFms(expr string) (starlark.Value, error) {
	return starlark.EvalOptions(dialect, newThread(nil), "", expr, starlark.StringDict{"fms": fmsModule()})
}

func testFms(t *testing.T, tests []struct{ expr, want string }) {
	t.Helper()
	for _, tc := range tests {
		v, err := evalFms(tc.expr)
		if err != nil {
			if msg := evalMessage(err); msg != tc.want {
				t.Errorf("%s: error %s, want %s", tc.expr, msg, tc.want)
			}
			continue
		}
		if got := v.String(); got != tc.want {
			t.Errorf("%s = %s, want %s", tc.expr, got, tc.want)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	testFms(t, []struct{ expr, want string }{
		{`fms.format_number(1234567)`, `"1234567"`},
		{`fms.format_number(1234567, sep=",")`, `"1,234,567"`},
		{`fms.format_number(1234567.891, sep=" ", point=",", decimals=2)`, `"1 234 567,89"`},
		{`fms.format_number(98.456, decimals=1)`, `"98.5"`},
		{`fms.format_number(0.125, decimals=2)`, `"0.12"`},
		{`fms.format_number(12, decimals=2)`, `"12.00"`},
		{`fms.format_number(5, digits=2)`, `"05"`},
		{`fms.format_number(1234, sep=",", digits=6)`, `"001,234"`},
		{`fms.format_number(-1234, sep=",")`, `"-1,234"`},
		{`fms.format_number(-0.4)`, `"0"`},
		{`fms.format_number(123456789012345678901234567890, sep=",")`, `"123,456,789,012,345,678,901,234,567,890"`},
		{`fms.format_number(1, decimals=21)`, "fms.format_number: decimals must be from 0 to 20, got 21"},
		{`fms.format_number(1, digits=0)`, "fms.format_number: digits must be from 1 to 20, got 0"},
		{`fms.format_number(float("nan"))`, "fms.format_number: cannot format nan"},
		{`fms.format_number("1")`, "fms.format_number: got string, want int or float"},
	})
}

func TestTimeFunctions(t *testing.T) {
	testFms(t, []struct{ expr, want string }{
		{`fms.split_duration(90061)`, "duration_parts(days = 1, hours = 1, minutes = 1, seconds = 1)"},
		{`fms.split_duration(-90.9)`, "duration_parts(days = 0, hours = 0, minutes = -1, seconds = -30)"},
		{`fms.split_duration(fms.parse_duration("49h30m15.5s"))`, "duration_parts(days = 2, hours = 1, minutes = 30, seconds = 15)"},
		{`fms.split_duration(1e30)`, "fms.split_duration: 1e+30 is out of range"},
		{`fms.split_duration("1h")`, "fms.split_duration: got string, want a duration or a number of seconds"},
		{`fms.parse_time("2026-12-01T18:00:00+01:00").unix`, "1796144400"},
		{`fms.parse_time("01/12/2026 18:00", format="02/01/2006 15:04", location="Europe/Paris").unix`, "1796144400"},
		{`fms.time(year=2026, month=12, day=1, hour=18, location="Europe/Paris").in_location("UTC").hour`, "17"},
		// Times are in UTC, unless given a location.
		{`fms.from_timestamp(0).format("2006-01-02T15:04:05Z07:00")`, `"1970-01-01T00:00:00Z"`},
		{`fms.is_valid_timezone("America/New_York")`, "True"},
		{`fms.parse_duration("1x")`, `fms.parse_duration: for parameter 1: time: unknown unit "x" in duration "1x"`},
		{`fms.parse_time()`, "fms.parse_time: missing argument for x"},
		{`fms.time(location="Nowhere/City")`, "fms.time: unknown time zone Nowhere/City"},
		{`fms.now()`, "module has no .now field or method"},
	})
}
