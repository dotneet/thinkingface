package tfcli

import (
	"errors"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

func untilTestRun() *apitypes.ExpRun {
	return &apitypes.ExpRun{
		Name:       "run-1",
		Status:     apitypes.RunStatusRunning,
		LastStep:   12000,
		NumPoints:  500,
		Summary:    map[string]float64{"loss": 0.25, "val/CER": 0.18, "val loss": 0.4, "a<b": 1, "acc": 0.9},
		SummaryMin: map[string]float64{"loss": 0.2, "val/CER": 0.15},
		SummaryMax: map[string]float64{"loss": 2.5, "acc": 0.95},
	}
}

func TestParseUntilEval(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"step>=12000", true},
		{"step > 12000", false},
		{"step==12000", true},
		{"step!=12000", false},
		{"step<12001", true},
		{"step<=11999", false},
		{"points>=500", true},
		{"STEP >= 1e4", true},
		{"status==running", true},
		{"status!=running", false},
		{"status==RUNNING", true},
		{"status == 'running'", true},
		{`status != "finished"`, true},
		{"metric:loss<0.3", true},
		{"last:loss<0.3", true},
		{"metric:val/CER<0.2", true},
		{`metric:"val/CER" < 0.2`, true},
		{`metric:"val loss" <= 0.4`, true},
		{`metric:'a<b' == 1`, true},
		{"min:loss<=0.2", true},
		{"max:loss>2", true},
		{"max:acc>=0.95", true},
		{"min:acc<1", false},         // min not logged for acc
		{"metric:missing!=1", false}, // unknown never satisfies
		{"metric:loss>-1", true},
		// precedence: and binds tighter than or
		{"step>0 or step<0 and status==finished", true},      // step>0 or (false)
		{"(step>0 or step<0) and status==finished", false},   // true and false
		{"status==finished and step<0 or points==500", true}, // (false) or true
		{"status==finished and (step<0 or points==500)", false},
		{"((step>=12000))", true},
		{"step>=1 AND points>=1 Or status==stale", true},
		{"step>=1 and points>=1000 or status==stale", false},
	}
	run := untilTestRun()
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			e, err := parseUntil(tc.expr)
			if err != nil {
				t.Fatalf("parseUntil(%q): %v", tc.expr, err)
			}
			if got := e.eval(run); got != tc.want {
				t.Errorf("eval(%q) = %v, want %v (parsed as %s)", tc.expr, got, tc.want, e)
			}
		})
	}
}

func TestParseUntilPrecedenceShape(t *testing.T) {
	cases := map[string]string{
		"step>1 or step>2 and step>3":         "(step>1 or (step>2 and step>3))",
		"step>1 and step>2 or step>3":         "((step>1 and step>2) or step>3)",
		"step>1 and (step>2 or step>3)":       "(step>1 and (step>2 or step>3))",
		"step>1 or step>2 or step>3":          "((step>1 or step>2) or step>3)",
		`metric:"val loss"<0.5`:               `metric:"val loss"<0.5`,
		"metric:val/CER<0.2":                  "metric:val/CER<0.2",
		"min:loss<=0.25 and status!=finished": "(min:loss<=0.25 and status!=finished)",
	}
	for in, want := range cases {
		e, err := parseUntil(in)
		if err != nil {
			t.Fatalf("parseUntil(%q): %v", in, err)
		}
		if got := e.String(); got != want {
			t.Errorf("parseUntil(%q) = %s, want %s", in, got, want)
		}
		// The rendering must itself parse back to the same thing.
		again, err := parseUntil(e.String())
		if err != nil || again.String() != want {
			t.Errorf("round trip of %q: %v, %v", e.String(), again, err)
		}
	}
}

func TestParseUntilErrors(t *testing.T) {
	cases := []struct {
		expr    string
		col     int // 1-based column of the error
		msgPart string
	}{
		{"", 1, "empty condition"},
		{"   ", 4, "empty condition"},
		{"step", 5, "expected a comparison operator"},
		{"step >", 7, "expected a value"},
		{"step > abc", 8, "expected a number"},
		{`step > "5"`, 8, "expected a number"},
		{"step = 5", 6, "unknown operator \"=\""},
		{"step ! 5", 6, "unknown operator \"!\""},
		{"stp > 5", 1, "unknown field"},
		{"metric > 5", 1, "needs a metric name"},
		{"metric: > 5", 1, "needs a metric name"},
		{`metric: "x" > 5`, 1, "needs a metric name"},
		{"step:x > 5", 1, "takes no"},
		{"status > running", 8, "only be compared with == or !="},
		{"status == runnning", 11, "unknown status"},
		{"step>1 and", 11, "expected a condition"},
		{"step>1 step>2", 8, "expected \"and\", \"or\""},
		{"(step>1", 8, "expected \")\" to close the \"(\" at column 1"},
		{"step>1)", 7, "unbalanced"},
		{`metric:"abc > 1`, 8, "unterminated"},
		{"and step>1", 1, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			_, err := parseUntil(tc.expr)
			if err == nil {
				t.Fatalf("parseUntil(%q) succeeded, want an error", tc.expr)
			}
			var uerr *untilError
			if !errors.As(err, &uerr) {
				t.Fatalf("error %T is not *untilError", err)
			}
			if uerr.pos+1 != tc.col {
				t.Errorf("column = %d, want %d (%v)", uerr.pos+1, tc.col, err)
			}
			if !strings.Contains(err.Error(), tc.msgPart) {
				t.Errorf("error %q does not mention %q", err, tc.msgPart)
			}
		})
	}
}

func TestUntilErrorCaret(t *testing.T) {
	_, err := parseUntil("step >= x")
	var uerr *untilError
	if !errors.As(err, &uerr) {
		t.Fatalf("err = %v", err)
	}
	want := "  step >= x\n          ^"
	if got := uerr.caret(); got != want {
		t.Errorf("caret =\n%s\nwant\n%s", got, want)
	}
}

func TestUntilMentionsStatus(t *testing.T) {
	cases := map[string]bool{
		"step>=1":                     false,
		"status!=running":             true,
		"step>=1 or status==finished": true,
		"(metric:x<1 and min:y>2)":    false,
	}
	for in, want := range cases {
		e, err := parseUntil(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.mentionsStatus(); got != want {
			t.Errorf("mentionsStatus(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestUntilEscapedQuote(t *testing.T) {
	e, err := parseUntil(`metric:"a\"b\\c" == 3`)
	if err != nil {
		t.Fatal(err)
	}
	run := &apitypes.ExpRun{Summary: map[string]float64{`a"b\c`: 3}}
	if !e.eval(run) {
		t.Errorf("escaped metric name did not match: %s", e)
	}
}
