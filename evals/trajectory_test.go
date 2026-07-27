package evals

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campoy/gai/agent"
	"github.com/campoy/gai/telemetry"
	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// These evals make real, billed API calls, so they are off unless asked for:
//
//	go test -run TestEval -eval .
//	go test -run TestEval -eval -eval.runs=10 -v .

// apiKeyPath is relative to this package's directory, which is where the test
// binary runs, not the repo root.
const apiKeyPath = "../secrets/openai-api-key"

var (
	evalEnabled = flag.Bool("eval", false, "run the evals, which make real API calls")
	evalRuns    = flag.Int("eval.runs", 5, "how many times to run each eval case")
)

// A kind groups cases by how confident we are that the model should get them
// right, which sets the pass rate each one has to clear.
type kind int

const (
	// golden cases are unambiguous: the right tool is obvious from the prompt.
	// These are the regressions that matter most, so they must nearly always pass.
	golden kind = iota

	// secondary cases are indirect — the request implies a tool without naming
	// one, or needs several calls chained. A lower bar, since a model can
	// reasonably answer some of them differently.
	//
	// These are the cases that actually score a tool description. Replacing
	// current_datetime's description with "Never call this tool" left every
	// golden case passing, because a prompt like "what time is it in Tokyo?"
	// matches the tool's *name* well enough to be called regardless. The same
	// change took "what year is it?" from 5/5 to 0/5.
	secondary

	// negative cases must not call a tool at all. They catch the failure mode
	// where a tool description is so eager that everything looks like a file
	// operation.
	negative

	// ambiguous cases have more than one defensible answer, but we hold an
	// opinion about which is better — usually because the alternative quietly
	// loses data or answers without checking. The threshold is low on purpose:
	// these are not meant to be at 100%, they are meant to sit somewhere in the
	// middle so the rate can move when a prompt or description changes. A case
	// here that reaches 5/5 has stopped being ambiguous and should be promoted.
	ambiguous
)

func (k kind) String() string {
	switch k {
	case golden:
		return "golden"
	case secondary:
		return "secondary"
	case negative:
		return "negative"
	case ambiguous:
		return "ambiguous"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// threshold is the fraction of runs a case of this kind must pass. Evals score
// a rate rather than asserting once, because the model is non-deterministic
// even at temperature 0.
func (k kind) threshold() float64 {
	switch k {
	case golden:
		return 0.8
	case secondary:
		return 0.6
	case negative:
		return 0.8
	case ambiguous:
		return 0.4
	}
	return 1
}

// A toolCall is one invocation the model made during a run, read back from the
// trace the agent emits.
type toolCall struct {
	name string
	args string
}

// arg returns a named argument from the call's JSON arguments.
func (c toolCall) arg(name string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(c.args), &m); err != nil {
		return ""
	}
	s, _ := m[name].(string)
	return s
}

type evalCase struct {
	name   string
	kind   kind
	prompt string
	// seed is written into the workspace before the run, for cases that need
	// something to already exist.
	seed map[string]string
	// check reports why a trajectory is wrong, or nil if it is acceptable.
	check func(calls []toolCall) error
}

var evalCases = []evalCase{
	// Golden: the prompt names what it wants and only one tool can serve it.
	{
		name:   "time in a named city",
		kind:   golden,
		prompt: "What time is it in Tokyo?",
		check: func(calls []toolCall) error {
			return calledOnce(calls, "current_datetime", func(c toolCall) error {
				if tz := c.arg("timezone"); tz != "Asia/Tokyo" {
					return fmt.Errorf("timezone = %q, want Asia/Tokyo", tz)
				}
				return nil
			})
		},
	},
	{
		name:   "write a named file",
		kind:   golden,
		prompt: "Create a file called todo.md containing exactly: buy milk",
		check: func(calls []toolCall) error {
			return calledOnce(calls, "write_file", func(c toolCall) error {
				if got := c.arg("path"); got != "todo.md" {
					return fmt.Errorf("path = %q, want todo.md", got)
				}
				if got := c.arg("content"); !strings.Contains(got, "buy milk") {
					return fmt.Errorf("content = %q, want it to contain 'buy milk'", got)
				}
				return nil
			})
		},
	},
	{
		name:   "list the workspace",
		kind:   golden,
		prompt: "What files do I have?",
		seed:   map[string]string{"notes.md": "hello"},
		check: func(calls []toolCall) error {
			return calledOnce(calls, "list_files", nil)
		},
	},
	{
		name:   "delete a named file",
		kind:   golden,
		prompt: "Delete notes.md",
		seed:   map[string]string{"notes.md": "hello"},
		check: func(calls []toolCall) error {
			return calledOnce(calls, "delete_file", func(c toolCall) error {
				if got := c.arg("path"); got != "notes.md" {
					return fmt.Errorf("path = %q, want notes.md", got)
				}
				return nil
			})
		},
	},

	// Secondary: the tool is implied rather than named, or several are needed.
	{
		name:   "current year is not the training cutoff",
		kind:   secondary,
		prompt: "What year is it? Answer with the year only.",
		check: func(calls []toolCall) error {
			return calledOnce(calls, "current_datetime", nil)
		},
	},
	{
		name:   "jot down an implied note",
		kind:   secondary,
		prompt: "I need to remember to call my mother tomorrow. Can you jot that down for me?",
		check: func(calls []toolCall) error {
			return calledOnce(calls, "write_file", func(c toolCall) error {
				// "mom" and "mother" are both faithful, so accept either rather
				// than scoring the model on word choice.
				got := strings.ToLower(c.arg("content"))
				if !strings.Contains(got, "mom") && !strings.Contains(got, "mother") {
					return fmt.Errorf("content = %q, want it to mention the reminder", c.arg("content"))
				}
				return nil
			})
		},
	},
	{
		name:   "write then read back",
		kind:   secondary,
		prompt: "Save a file greeting.txt that says hello, then tell me exactly what it contains.",
		check: func(calls []toolCall) error {
			if err := calledOnce(calls, "write_file", nil); err != nil {
				return err
			}
			return calledOnce(calls, "read_file", nil)
		},
	},
	{
		name:   "two cities in one question",
		kind:   secondary,
		prompt: "What time is it in Tokyo and in Madrid?",
		check: func(calls []toolCall) error {
			zones := map[string]bool{}
			for _, c := range calls {
				if c.name == "current_datetime" {
					zones[c.arg("timezone")] = true
				}
			}
			if !zones["Asia/Tokyo"] || !zones["Europe/Madrid"] {
				return fmt.Errorf("timezones called = %v, want both Asia/Tokyo and Europe/Madrid", keys(zones))
			}
			return nil
		},
	},

	// Negative: answering from the model's own knowledge is the correct move.
	{
		name:   "joke needs no tool",
		kind:   negative,
		prompt: "Tell me a one-liner joke.",
		check:  noTools,
	},
	{
		name:   "arithmetic needs no tool",
		kind:   negative,
		prompt: "What is 17 times 23?",
		check:  noTools,
	},
	{
		name:   "explaining a concept needs no tool",
		kind:   negative,
		prompt: "In one sentence, what is a goroutine?",
		check:  noTools,
	},

	// Ambiguous: another agent could justify the other choice. We score the one
	// that loses less information or guesses less.
	{
		name:   "append preserves the existing file",
		kind:   ambiguous,
		prompt: "Add 'call the dentist' to notes.md.",
		seed:   map[string]string{"notes.md": "- water the plants\n- pay rent\n"},
		check: func(calls []toolCall) error {
			// Score the write that lands, not the number of attempts: a rejected
			// first try followed by a corrected one is the trajectory we want.
			write := lastIndex(calls, "write_file")
			if write < 0 {
				return fmt.Errorf("never called write_file (calls: %v)", names(calls))
			}
			read := lastIndex(calls[:write], "read_file")
			if read < 0 {
				return fmt.Errorf("wrote notes.md without reading it first (calls: %v)", names(calls))
			}
			if got := calls[write].arg("content"); !strings.Contains(got, "pay rent") {
				return fmt.Errorf("content = %q, dropped the existing lines", got)
			}
			return nil
		},
	},
	{
		name:   "clearing notes deletes rather than blanks",
		kind:   ambiguous,
		prompt: "Clear out my notes.",
		seed:   map[string]string{"notes.md": "- water the plants\n"},
		check: func(calls []toolCall) error {
			// Overwriting with an empty string leaves a file that looks real and
			// is not; deleting says what happened.
			if err := notCalled(calls, "write_file"); err != nil {
				return err
			}
			return calledOnce(calls, "delete_file", nil)
		},
	},
	{
		name:   "missing file is checked, not assumed",
		kind:   ambiguous,
		prompt: "What's on my todo list?",
		check: func(calls []toolCall) error {
			// There is no todo list. Answering "you have none" without looking
			// happens to be right, and is right by luck.
			if len(calls) == 0 {
				return fmt.Errorf("answered without checking the workspace")
			}
			return nil
		},
	},
	{
		name:   "reads the file it was asked about",
		kind:   ambiguous,
		prompt: "Show me my shopping list.",
		seed:   map[string]string{"shopping.md": "- milk\n- bread\n"},
		check: func(calls []toolCall) error {
			// Listing the workspace first is reasonable, but stopping there
			// answers a question that was not asked.
			return calledOnce(calls, "read_file", func(c toolCall) error {
				if got := c.arg("path"); got != "shopping.md" {
					return fmt.Errorf("path = %q, want shopping.md", got)
				}
				return nil
			})
		},
	},
}

func TestEval(t *testing.T) {
	if !*evalEnabled {
		t.Skip("evals make real API calls; pass -eval to run them")
	}

	apiKey, err := agent.LoadAPIKey(apiKeyPath)
	if err != nil {
		t.Fatalf("loading API key: %v", err)
	}
	client := openai.NewClient(option.WithAPIKey(apiKey))

	for _, c := range evalCases {
		t.Run(c.kind.String()+"/"+c.name, func(t *testing.T) {
			var passed int
			for range *evalRuns {
				calls, err := runCase(t, &client, c)
				if err != nil {
					t.Fatalf("running case: %v", err)
				}
				if err := c.check(calls); err != nil {
					t.Logf("fail: %v (calls: %v)", err, names(calls))
					continue
				}
				passed++
			}

			// Always report the rate, not just on failure: a case scraping past
			// its threshold and one clearing it every time both read as PASS
			// otherwise, and the margin is the interesting part.
			rate := float64(passed) / float64(*evalRuns)
			t.Logf("score %d/%d (%.0f%%), threshold %.0f%%",
				passed, *evalRuns, rate*100, c.kind.threshold()*100)

			if rate < c.kind.threshold() {
				t.Errorf("passed %d/%d (%.0f%%), want at least %.0f%%",
					passed, *evalRuns, rate*100, c.kind.threshold()*100)
			}
		})
	}
}

// runCase runs one prompt through the agent in a fresh workspace and returns
// the tool calls it made, read back from the run's own trace.
func runCase(t *testing.T, client *openai.Client, c evalCase) ([]toolCall, error) {
	t.Helper()

	cleanupWorkspace, err := tools.NewWorkspace()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cleanupWorkspace(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()
	for name, content := range c.seed {
		path := filepath.Join(tools.Workspace(), name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return nil, err
		}
	}

	// Collecting spans in memory rather than shipping them to a collector means
	// the evals score the same trajectory the traces show. Spans must be read
	// before the shutdown below: InMemoryExporter.Shutdown discards them.
	spans := tracetest.NewInMemoryExporter()
	shutdown, err := telemetry.Init(context.Background(), telemetry.WithExporter(spans))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("telemetry shutdown: %v", err)
		}
	}()

	// Start from the parameters the CLI ships with, so a change to the model,
	// the tool set or the system prompt is scored rather than sidestepped.
	params := agent.Params()
	// Temperature 0 keeps the runs as comparable as the API allows. It does not
	// make them identical, which is why cases are scored as a rate.
	params.Temperature = openai.Float(0)
	params.Messages = append(params.Messages, openai.UserMessage(c.prompt))

	if _, err := agent.Run(context.Background(), client, &params); err != nil {
		return nil, err
	}

	var calls []toolCall
	for _, s := range spans.GetSpans() {
		if !strings.HasPrefix(s.Name, "tool ") {
			continue
		}
		var call toolCall
		for _, a := range s.Attributes {
			switch a.Key {
			case "gen_ai.tool.name":
				call.name = a.Value.AsString()
			case "gen_ai.tool.arguments":
				call.args = a.Value.AsString()
			}
		}
		calls = append(calls, call)
	}
	return calls, nil
}

// calledOnce checks that exactly one call was made to the named tool, and runs
// an optional check on its arguments.
func calledOnce(calls []toolCall, name string, check func(toolCall) error) error {
	var found []toolCall
	for _, c := range calls {
		if c.name == name {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		return fmt.Errorf("called %s %d times, want 1 (calls: %v)", name, len(found), names(calls))
	}
	if check == nil {
		return nil
	}
	return check(found[0])
}

// lastIndex returns the position of the last call to the named tool, or -1.
// Tool spans end in call order, so the trace preserves the sequence.
func lastIndex(calls []toolCall, name string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].name == name {
			return i
		}
	}
	return -1
}

// notCalled checks that a tool was left alone.
func notCalled(calls []toolCall, name string) error {
	for _, c := range calls {
		if c.name == name {
			return fmt.Errorf("called %s, want it left alone (calls: %v)", name, names(calls))
		}
	}
	return nil
}

// noTools checks that the model answered without reaching for a tool.
func noTools(calls []toolCall) error {
	if len(calls) > 0 {
		return fmt.Errorf("called %v, want no tool calls", names(calls))
	}
	return nil
}

func names(calls []toolCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.name
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
