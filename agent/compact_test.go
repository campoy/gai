package agent

import (
	"fmt"
	"testing"

	"github.com/openai/openai-go"
)

// The message shapes a conversation is built from. Constructing them by hand
// keeps these tests free of the API: cutPoint is pure, and the rule it has to
// obey — never separate a tool call from its result — is checkable on a slice.

func system(content string) openai.ChatCompletionMessageParamUnion {
	return openai.SystemMessage(content)
}

func user(content string) openai.ChatCompletionMessageParamUnion {
	return openai.UserMessage(content)
}

func assistant(content string) openai.ChatCompletionMessageParamUnion {
	return openai.AssistantMessage(content)
}

// calls builds an assistant message that asks for one tool call per id.
func calls(ids ...string) openai.ChatCompletionMessageParamUnion {
	var tcs []openai.ChatCompletionMessageToolCallParam
	for _, id := range ids {
		tcs = append(tcs, openai.ChatCompletionMessageToolCallParam{
			ID: id,
			Function: openai.ChatCompletionMessageToolCallFunctionParam{
				Name:      "read_file",
				Arguments: fmt.Sprintf(`{"path":%q}`, id+".md"),
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{
		OfAssistant: &openai.ChatCompletionAssistantMessageParam{ToolCalls: tcs},
	}
}

func result(id string) openai.ChatCompletionMessageParamUnion {
	return openai.ToolMessage("contents of "+id, id)
}

// turn is one exchange: a user message, an assistant message that calls a tool,
// its result, and a reply. Enough to make every conversation below contain the
// pairing that a careless cut would break.
func turn(prompt, id string) []openai.ChatCompletionMessageParamUnion {
	return []openai.ChatCompletionMessageParamUnion{
		user(prompt), calls(id), result(id), assistant("done"),
	}
}

func conversation(turns ...[]openai.ChatCompletionMessageParamUnion) []openai.ChatCompletionMessageParamUnion {
	msgs := []openai.ChatCompletionMessageParamUnion{system(SystemPrompt)}
	for _, t := range turns {
		msgs = append(msgs, t...)
	}
	return msgs
}

func TestCutPoint(t *testing.T) {
	tests := []struct {
		name string
		msgs []openai.ChatCompletionMessageParamUnion
		want int
	}{
		{
			name: "empty conversation",
			msgs: nil,
			want: 0,
		},
		{
			name: "system prompt only",
			msgs: conversation(),
			want: 0,
		},
		{
			name: "fewer turns than are kept",
			msgs: conversation(turn("one", "a")),
			want: 0,
		},
		{
			name: "exactly the turns that are kept",
			msgs: conversation(turn("one", "a"), turn("two", "b")),
			want: 0,
		},
		{
			// Three turns of four messages: the user messages sit at 1, 5 and 9,
			// and keeping the last two means the cut lands on the second.
			name: "one turn more than is kept",
			msgs: conversation(turn("one", "a"), turn("two", "b"), turn("three", "c")),
			want: 5,
		},
		{
			name: "several turns to drop",
			msgs: conversation(
				turn("one", "a"), turn("two", "b"), turn("three", "c"),
				turn("four", "d"), turn("five", "e"),
			),
			want: 13,
		},
		{
			// One user message whose tools returned a great deal of text has no
			// interior boundary to cut at, so compaction has to decline.
			name: "a single turn, however long",
			msgs: conversation([]openai.ChatCompletionMessageParamUnion{
				user("read everything"),
				calls("a", "b", "c"), result("a"), result("b"), result("c"),
				calls("d"), result("d"),
				assistant("here you go"),
			}),
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cutPoint(tt.msgs)
			if got != tt.want {
				t.Fatalf("cutPoint() = %d, want %d", got, tt.want)
			}
			if got == 0 {
				return
			}
			if tt.msgs[got].OfUser == nil {
				t.Errorf("cut at %d, which is not a user message", got)
			}
		})
	}
}

// TestCutPointKeepsHistoryValid is the test that matters: whatever cutPoint
// returns, the history that survives has to be one the API will accept. A tool
// result whose call was dropped, or a call whose results were, is rejected
// outright — so a bad cut does not degrade the agent, it breaks it.
func TestCutPointKeepsHistoryValid(t *testing.T) {
	conversations := map[string][]openai.ChatCompletionMessageParamUnion{
		"plain turns": conversation(
			turn("one", "a"), turn("two", "b"), turn("three", "c"), turn("four", "d"),
		),
		"several calls in one turn": conversation(
			[]openai.ChatCompletionMessageParamUnion{
				user("read both"), calls("a", "b"), result("a"), result("b"), assistant("read"),
			},
			turn("two", "c"),
			[]openai.ChatCompletionMessageParamUnion{
				user("and again"), calls("d", "e"), result("d"), result("e"), assistant("read"),
			},
			turn("four", "f"),
		),
		"calls chained within a turn": conversation(
			[]openai.ChatCompletionMessageParamUnion{
				user("list then read"), calls("a"), result("a"), calls("b"), result("b"), assistant("done"),
			},
			turn("two", "c"),
			turn("three", "d"),
		),
		"a turn the model answered without tools": conversation(
			[]openai.ChatCompletionMessageParamUnion{user("hello"), assistant("hi")},
			turn("two", "a"),
			turn("three", "b"),
		),
	}

	for name, msgs := range conversations {
		t.Run(name, func(t *testing.T) {
			cut := cutPoint(msgs)
			if cut == 0 {
				t.Fatalf("nothing was dropped from a conversation of %d messages", len(msgs))
			}

			// What compact would build: the system prompt, a summary standing in
			// for the middle, then everything from the cut onwards.
			kept := []openai.ChatCompletionMessageParamUnion{msgs[0], system("summary")}
			kept = append(kept, msgs[cut:]...)

			if err := validateToolPairing(kept); err != nil {
				t.Errorf("cut at %d left an invalid history: %v", cut, err)
			}
			// The dropped half has to stand on its own too — it is sent to the
			// model to be summarised, and the same rules apply to that request.
			if err := validateToolPairing(msgs[1:cut]); err != nil {
				t.Errorf("cut at %d left an invalid stretch to summarise: %v", cut, err)
			}
		})
	}
}

func TestCutPointKeepsTheSystemPrompt(t *testing.T) {
	msgs := conversation(turn("one", "a"), turn("two", "b"), turn("three", "c"))
	cut := cutPoint(msgs)
	if cut == 0 {
		t.Fatal("expected a cut")
	}
	// Message 0 is never inside the dropped range, so the persona survives
	// whatever else goes.
	if cut <= 0 {
		t.Fatalf("cut at %d would drop the system prompt", cut)
	}
	if msgs[0].OfSystem == nil {
		t.Fatal("message 0 is not the system prompt")
	}
}

func TestCutPointKeepsTheRecentTurns(t *testing.T) {
	msgs := conversation(
		turn("one", "a"), turn("two", "b"), turn("three", "c"), turn("four", "d"),
	)
	cut := cutPoint(msgs)

	var kept int
	for _, m := range msgs[cut:] {
		if m.OfUser != nil {
			kept++
		}
	}
	if kept != keepTurns {
		t.Errorf("kept %d user messages, want %d", kept, keepTurns)
	}
	// And the last one is still the last one: the tail is kept verbatim, not
	// rebuilt.
	last := msgs[len(msgs)-1]
	if last.OfAssistant == nil {
		t.Fatal("expected the conversation to end with an assistant message")
	}
}

// validateToolPairing reports whether a message slice pairs up tool calls and
// their results the way the API insists on: every result answers a call in the
// assistant message just before it, and every call is answered before the
// conversation moves on.
//
// This is the rule a careless cut breaks, and the reason a bad cut does not
// merely degrade the agent — the request is rejected outright.
func validateToolPairing(msgs []openai.ChatCompletionMessageParamUnion) error {
	outstanding := map[string]bool{}
	for i, m := range msgs {
		switch {
		case m.OfAssistant != nil:
			if len(outstanding) > 0 {
				return fmt.Errorf("message %d: assistant turn with %d tool calls still unanswered", i, len(outstanding))
			}
			for _, tc := range m.OfAssistant.ToolCalls {
				outstanding[tc.ID] = true
			}

		case m.OfTool != nil:
			id := m.OfTool.ToolCallID
			if !outstanding[id] {
				return fmt.Errorf("message %d: result for tool call %q, which was never made", i, id)
			}
			delete(outstanding, id)

		default:
			if len(outstanding) > 0 {
				return fmt.Errorf("message %d: %d tool calls left unanswered", i, len(outstanding))
			}
		}
	}
	if len(outstanding) > 0 {
		return fmt.Errorf("%d tool calls left unanswered at the end", len(outstanding))
	}
	return nil
}

// TestValidateToolPairingCatchesABadCut guards the guard: a check that accepts
// anything would make the tests above pass no matter what cutPoint did.
//
// Cutting at a tool result is the mistake to catch. It leaves an answer to a
// call that is no longer in the history, which the API rejects outright.
func TestValidateToolPairingCatchesABadCut(t *testing.T) {
	msgs := conversation(turn("one", "a"), turn("two", "b"))

	var checked int
	for cut, m := range msgs {
		if m.OfTool == nil {
			continue
		}
		checked++
		kept := append([]openai.ChatCompletionMessageParamUnion{msgs[0]}, msgs[cut:]...)
		if err := validateToolPairing(kept); err == nil {
			t.Errorf("cutting at %d (a tool result) should have been rejected", cut)
		}
	}
	if checked == 0 {
		t.Fatal("the conversation contained no tool results to cut at")
	}
}
