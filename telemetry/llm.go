package telemetry

import (
	"context"
	"fmt"

	"github.com/openai/openai-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Laminar reads these attributes to render a span as a model call rather than
// a plain one. They follow the OpenTelemetry GenAI conventions, except for the
// lmnr.* keys, which are Laminar's own. Collectors that don't know them, such
// as Jaeger, just show them as ordinary attributes.
const (
	spanTypeKey      = attribute.Key("lmnr.span.type")
	systemKey        = attribute.Key("gen_ai.system")
	requestModelKey  = attribute.Key("gen_ai.usage.request_model")
	responseModelKey = attribute.Key("gen_ai.usage.response_model")
	inputTokensKey   = attribute.Key("gen_ai.usage.input_tokens")
	outputTokensKey  = attribute.Key("gen_ai.usage.output_tokens")
)

// StartLLM starts a span covering one call to the model, recording the model
// requested and the full prompt. The caller must pass the returned span to
// EndLLM.
func StartLLM(ctx context.Context, model string, msgs []openai.ChatCompletionMessageParamUnion) (context.Context, trace.Span) {
	ctx, span := Tracer().Start(ctx, "chat "+model, trace.WithAttributes(
		spanTypeKey.String("LLM"),
		systemKey.String("openai"),
		requestModelKey.String(model),
	))
	for i, m := range msgs {
		role, content := message(m)
		span.SetAttributes(
			attribute.String(fmt.Sprintf("gen_ai.prompt.%d.role", i), role),
			attribute.String(fmt.Sprintf("gen_ai.prompt.%d.content", i), content),
		)
	}
	return ctx, span
}

// EndLLM records the model's reply and token usage, then ends the span. A nil
// completion means the call failed, and err is recorded instead.
func EndLLM(span trace.Span, completion *openai.ChatCompletion, err error) {
	defer span.End()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	span.SetAttributes(
		responseModelKey.String(completion.Model),
		inputTokensKey.Int64(completion.Usage.PromptTokens),
		outputTokensKey.Int64(completion.Usage.CompletionTokens),
	)
	for i, c := range completion.Choices {
		span.SetAttributes(
			attribute.String(fmt.Sprintf("gen_ai.completion.%d.role", i), "assistant"),
			attribute.String(fmt.Sprintf("gen_ai.completion.%d.content", i), c.Message.Content),
		)
		for j, tc := range c.Message.ToolCalls {
			span.SetAttributes(
				attribute.String(fmt.Sprintf("gen_ai.completion.%d.tool_calls.%d.name", i, j), tc.Function.Name),
				attribute.String(fmt.Sprintf("gen_ai.completion.%d.tool_calls.%d.arguments", i, j), tc.Function.Arguments),
			)
		}
	}
}

// StartTool starts a span covering one tool invocation.
func StartTool(ctx context.Context, name, args string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "tool "+name, trace.WithAttributes(
		spanTypeKey.String("TOOL"),
		attribute.String("gen_ai.tool.name", name),
		attribute.String("gen_ai.tool.arguments", args),
	))
}

// message flattens a message union into the role and text content the GenAI
// conventions expect. Multi-part content is reported as its part count, since
// this agent only ever sends plain strings.
func message(m openai.ChatCompletionMessageParamUnion) (role, content string) {
	switch {
	case m.OfSystem != nil:
		return "system", m.OfSystem.Content.OfString.Or(multipart(len(m.OfSystem.Content.OfArrayOfContentParts)))
	case m.OfUser != nil:
		return "user", m.OfUser.Content.OfString.Or(multipart(len(m.OfUser.Content.OfArrayOfContentParts)))
	case m.OfAssistant != nil:
		return "assistant", m.OfAssistant.Content.OfString.Or(multipart(len(m.OfAssistant.Content.OfArrayOfContentParts)))
	case m.OfTool != nil:
		return "tool", m.OfTool.Content.OfString.Or(multipart(len(m.OfTool.Content.OfArrayOfContentParts)))
	case m.OfDeveloper != nil:
		return "developer", m.OfDeveloper.Content.OfString.Or(multipart(len(m.OfDeveloper.Content.OfArrayOfContentParts)))
	default:
		return "unknown", ""
	}
}

func multipart(parts int) string {
	if parts == 0 {
		return ""
	}
	return fmt.Sprintf("<%d content parts>", parts)
}
