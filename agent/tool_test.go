package agent

import (
	"context"
	"testing"

	"github.com/campoy/gai/telemetry"
	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// A tool is handed the context the tool span was started from, so that anything
// it does of its own — web_search runs a second model call — is cancelled with
// the run and recorded beneath the call that caused it. Both halves of that are
// invisible in the tool's own result, which is why they are pinned here: the
// agent kept working for months while passing context.Background() instead.
func TestRunToolHandsTheToolTheSpanContext(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	shutdown, err := telemetry.Init(t.Context(), telemetry.WithExporter(spans))
	if err != nil {
		t.Fatalf("telemetry.Init: %v", err)
	}
	t.Cleanup(func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("shutting telemetry down: %v", err)
		}
	})

	// The tool keeps the context rather than inspecting it, so the assertions
	// below can outlive the call the way a cancelled request would.
	var got context.Context
	probe := tools.New("probe", "records the context it was called with", nil,
		func(ctx context.Context, _ string) (string, error) {
			got = ctx
			return "ok", nil
		})
	a := &Agent{tools: tools.Tools{probe}}

	ctx, cancel := context.WithCancel(t.Context())
	out := a.runTool(ctx, openai.ChatCompletionMessageToolCall{
		ID: "call-1",
		Function: openai.ChatCompletionMessageToolCallFunction{
			Name:      "probe",
			Arguments: `{}`,
		},
	})
	if out != "ok" {
		t.Fatalf("runTool = %q, want %q", out, "ok")
	}
	if got == nil {
		t.Fatal("the tool was called with no context at all")
	}

	// Same span the trace records for the call, so a span the tool starts is a
	// child of it rather than a second root.
	recorded := spans.GetSpans()
	if len(recorded) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(recorded))
	}
	if want, got := recorded[0].SpanContext.SpanID(), trace.SpanContextFromContext(got).SpanID(); got != want {
		t.Errorf("tool ran under span %s, want the tool span %s", got, want)
	}

	// And it is the caller's context, not a fresh one: cancelling the run
	// reaches whatever the tool is still doing with it.
	if err := got.Err(); err != nil {
		t.Fatalf("the tool's context was already done: %v", err)
	}
	cancel()
	if err := got.Err(); err == nil {
		t.Error("cancelling the run did not reach the tool's context")
	}
}
