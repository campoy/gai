package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// The loop is driven against a stub server rather than the API. That keeps this
// test in `go test ./...` — which must stay unbilled — while still covering the
// part cutPoint tests cannot: that usage from one response triggers compaction
// before the next request, and that the request which follows carries the
// spliced history rather than the original.

// request is as much of a chat completion request as these tests inspect.
type request struct {
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
}

func (r request) roles() string {
	var roles []string
	for _, m := range r.Messages {
		roles = append(roles, m.Role)
	}
	return strings.Join(roles, ",")
}

func (r request) text(i int) string {
	s, _ := r.Messages[i].Content.(string)
	return s
}

// reply is one canned response: what the model says, and what it claims the
// conversation cost.
type reply struct {
	content    string
	totalToken int64
}

// stub serves replies in order and records the requests that asked for them.
func stub(t *testing.T, replies ...reply) (*openai.Client, *[]request) {
	t.Helper()

	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got = append(got, req)

		if len(got) > len(replies) {
			t.Errorf("request %d, but only %d replies were canned", len(got), len(replies))
			http.Error(w, "no reply left", http.StatusInternalServerError)
			return
		}
		rep := replies[len(got)-1]

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"id": "stub", "object": "chat.completion", "created": 0, "model": %q,
			"choices": [{"index": 0, "message": {"role": "assistant", "content": %q}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": %d}
		}`, Model, rep.content, rep.totalToken)
	}))
	t.Cleanup(srv.Close)

	client := openai.NewClient(
		option.WithAPIKey("stub"),
		option.WithBaseURL(srv.URL+"/"),
		option.WithMaxRetries(0),
	)
	return &client, &got
}

func TestRunCompactsOnceTheBudgetIsPassed(t *testing.T) {
	// Three messages through one conversation. Nothing is dropped until there
	// are more user messages than keepTurns, so compaction lands on the third.
	client, requests := stub(t,
		reply{content: "first", totalToken: compactAfter + 1},
		reply{content: "second", totalToken: compactAfter + 1},
		reply{content: "a summary of the earlier turns", totalToken: 10},
		reply{content: "third", totalToken: 10},
	)

	// No workspace: these cases drive the loop against a stubbed API and never
	// reach a file tool.
	a := New(client, nil)
	params := a.Params()
	for _, message := range []string{"one", "two", "three"} {
		params.Messages = append(params.Messages, openai.UserMessage(message))
		if _, err := a.Run(context.Background(), &params); err != nil {
			t.Fatalf("run %q: %v", message, err)
		}
	}

	if len(*requests) != 4 {
		t.Fatalf("made %d requests, want 4 (three turns plus one summary)", len(*requests))
	}

	// The first two turns are sent whole: over budget, but with too few user
	// messages for a cut to drop anything.
	if got, want := (*requests)[0].roles(), "system,user"; got != want {
		t.Errorf("first request roles = %q, want %q", got, want)
	}
	if got, want := (*requests)[1].roles(), "system,user,assistant,user"; got != want {
		t.Errorf("second request roles = %q, want %q", got, want)
	}

	// The third request is the summary call: its own prompt, the transcript, and
	// no history of its own.
	summary := (*requests)[2]
	if got, want := summary.roles(), "system,user"; got != want {
		t.Fatalf("summary request roles = %q, want %q", got, want)
	}
	if summary.text(0) != summaryPrompt {
		t.Errorf("summary request was not sent the summariser prompt")
	}
	if !strings.Contains(summary.text(1), "USER: one") {
		t.Errorf("summary request did not include the dropped turns, got:\n%s", summary.text(1))
	}
	if strings.Contains(summary.text(1), "USER: three") {
		t.Errorf("summary request included a turn that was meant to be kept")
	}

	// And the turn that follows carries the compacted history: the persona, the
	// summary, then the two most recent user messages and what came between.
	final := (*requests)[3]
	if got, want := final.roles(), "system,system,user,assistant,user"; got != want {
		t.Fatalf("final request roles = %q, want %q", got, want)
	}
	if final.text(0) != SystemPrompt {
		t.Errorf("the system prompt did not survive compaction")
	}
	if !strings.HasPrefix(final.text(1), summaryPreamble) {
		t.Errorf("message 1 is not the summary, got %q", final.text(1))
	}
	if !strings.Contains(final.text(1), "a summary of the earlier turns") {
		t.Errorf("the summary text was not spliced in, got %q", final.text(1))
	}
	if final.text(2) != "two" || final.text(4) != "three" {
		t.Errorf("recent turns were not kept verbatim, got %q and %q", final.text(2), final.text(4))
	}

	// The caller's params were compacted too — that is the documented contract.
	if len(params.Messages) != 6 {
		t.Errorf("caller holds %d messages after compaction, want 6", len(params.Messages))
	}
}

func TestRunDoesNotCompactUnderBudget(t *testing.T) {
	client, requests := stub(t,
		reply{content: "first", totalToken: compactAfter - 1},
		reply{content: "second", totalToken: compactAfter - 1},
		reply{content: "third", totalToken: compactAfter - 1},
	)

	// No workspace: these cases drive the loop against a stubbed API and never
	// reach a file tool.
	a := New(client, nil)
	params := a.Params()
	for _, message := range []string{"one", "two", "three"} {
		params.Messages = append(params.Messages, openai.UserMessage(message))
		if _, err := a.Run(context.Background(), &params); err != nil {
			t.Fatalf("run %q: %v", message, err)
		}
	}

	if len(*requests) != 3 {
		t.Fatalf("made %d requests, want 3 — a summary call means it compacted", len(*requests))
	}
	if got, want := (*requests)[2].roles(), "system,user,assistant,user,assistant,user"; got != want {
		t.Errorf("final request roles = %q, want %q", got, want)
	}
}

// A failed summary must not cost the caller their conversation.
func TestRunSurvivesAFailedSummary(t *testing.T) {
	client, requests := stub(t,
		reply{content: "first", totalToken: compactAfter + 1},
		reply{content: "second", totalToken: compactAfter + 1},
		// The summary comes back empty, which summarize rejects.
		reply{content: "", totalToken: 10},
		reply{content: "third", totalToken: 10},
	)

	// No workspace: these cases drive the loop against a stubbed API and never
	// reach a file tool.
	a := New(client, nil)
	params := a.Params()
	for _, message := range []string{"one", "two", "three"} {
		params.Messages = append(params.Messages, openai.UserMessage(message))
		if _, err := a.Run(context.Background(), &params); err != nil {
			t.Fatalf("run %q: %v", message, err)
		}
	}

	// The history is untouched: over budget, but whole, and the conversation
	// carried on.
	final := (*requests)[3]
	if got, want := final.roles(), "system,user,assistant,user,assistant,user"; got != want {
		t.Errorf("final request roles = %q, want %q", got, want)
	}
}
