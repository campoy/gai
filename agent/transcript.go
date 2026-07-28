package agent

import (
	"fmt"
	"strings"

	"github.com/openai/openai-go"
)

// Transcribe renders a conversation as plain text: every user message, every
// assistant reply, and every tool call paired with the result it returned.
//
// The tool traffic is the point. Without it a reader — the judge in the evals,
// or the model asked to summarise a conversation in compact — can only take the
// assistant's word for what happened.
//
// It lives here rather than in the evals because compaction needs it too, and
// two renderings of the same conversation would drift apart.
func Transcribe(messages []openai.ChatCompletionMessageParamUnion) string {
	// Tool results carry only the id of the call they answer, so the names have
	// to be collected on the way past.
	toolNames := map[string]string{}

	// The first system message is the persona, and it is left out: a reader told
	// to grade or summarise conduct does not need the instruction to be
	// flamboyant, and showing it invites grading tone. Any later system message
	// is a summary left behind by compaction, which does need to be shown —
	// otherwise turns appear to vanish with no explanation.
	seenSystem := false

	var b strings.Builder
	for _, m := range messages {
		switch {
		case m.OfSystem != nil:
			if !seenSystem {
				seenSystem = true
				continue
			}
			fmt.Fprintf(&b, "EARLIER CONVERSATION, SUMMARISED: %s\n\n", m.OfSystem.Content.OfString.Or(""))

		case m.OfUser != nil:
			fmt.Fprintf(&b, "USER: %s\n\n", m.OfUser.Content.OfString.Or(""))

		case m.OfAssistant != nil:
			if content := m.OfAssistant.Content.OfString.Or(""); content != "" {
				fmt.Fprintf(&b, "ASSISTANT: %s\n\n", content)
			}
			for _, tc := range m.OfAssistant.ToolCalls {
				toolNames[tc.ID] = tc.Function.Name
				fmt.Fprintf(&b, "ASSISTANT CALLS TOOL: %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
			}

		case m.OfTool != nil:
			name := toolNames[m.OfTool.ToolCallID]
			if name == "" {
				name = "unknown"
			}
			fmt.Fprintf(&b, "TOOL RESULT from %s: %s\n\n", name, m.OfTool.Content.OfString.Or(""))
		}
	}
	return b.String()
}
