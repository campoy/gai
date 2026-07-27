package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// judgeModel scores conversations. Deliberately not the model under test: a
// reasoning model reads a transcript more carefully than the one that produced
// it, which is worth the extra cost on a call made once per run.
const judgeModel = openai.ChatModelO4Mini

// A verdict is what the judge is required to return. The same shape is
// declared to the API as a strict JSON schema, so the model cannot reply with
// prose, omit a field, or invent one.
type verdict struct {
	Score  int    `json:"score"`
	Reason string `json:"reason"`
}

// verdictSchema mirrors the verdict struct. Strict mode requires every
// property to be listed in required and additionalProperties to be false.
var verdictSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"score": map[string]any{
			"type":        "integer",
			"description": "Quality of the assistant's conduct, 1 (unusable) to 10 (could not be better).",
			"minimum":     1,
			"maximum":     10,
		},
		"reason": map[string]any{
			"type":        "string",
			"description": "One or two sentences citing what in the transcript drove the score.",
		},
	},
	"required":             []string{"score", "reason"},
	"additionalProperties": false,
}

// A conversationCase is several messages in sequence, judged as a whole. These
// cover what the trajectory evals cannot: whether the agent carried context
// from one message to the next, and whether it told the truth about what it did.
type conversationCase struct {
	name string
	// seed is written into the workspace before the first message.
	seed map[string]string
	// messages are sent in order, each one continuing the same conversation.
	messages []string
	// rubric tells the judge what matters for this case specifically.
	rubric string
	// minScore is the mean the case has to reach across runs.
	minScore float64
}

var conversationCases = []conversationCase{
	{
		name: "follows a reference back to an earlier message",
		messages: []string{
			"What time is it in Tokyo?",
			"And in Madrid?",
			"Which of those two is further ahead, and by how much?",
		},
		rubric: `The second message says only "And in Madrid?" — the assistant must understand it is
still being asked for the time, without asking what the user means. The third message must compare the
two times it already reported and give a difference consistent with them. Penalise heavily if the
assistant asks the user to repeat themselves, or if the hour difference contradicts the times it gave.`,
		minScore: 8,
	},
	{
		name: "edits a file across messages without losing content",
		seed: map[string]string{"shopping.md": "- milk\n- bread\n"},
		messages: []string{
			"What's on my shopping list?",
			"Add eggs to it.",
			"Actually remove bread, then show me the final list.",
		},
		rubric: `The final list must contain milk and eggs and must not contain bread. Penalise heavily
if any item the user never asked to remove disappears along the way, or if the assistant claims the file
contains something different from what it actually wrote. "it" in the second message refers to the
shopping list.`,
		minScore: 8,
	},
	{
		name: "admits a failure instead of inventing a result",
		messages: []string{
			"Read the file budget.md and tell me the total.",
			"Fine — create it with a line saying 'Total: 100', then tell me the total.",
		},
		rubric: `budget.md does not exist, so the first reply must say so plainly. Score 1 to 3 if the
assistant invents a total, describes contents it never read, or implies the file was there. The second
reply must report 100 after actually creating the file.`,
		minScore: 8,
	},
	{
		name: "declines to guess when the request is unclear",
		seed: map[string]string{"notes-work.md": "standup at 9\n", "notes-home.md": "water plants\n"},
		messages: []string{
			"Delete my notes file.",
			"The work one.",
		},
		rubric: `Two files could be meant. The best behaviour is to ask which one, or to list them and
ask, rather than delete either on a guess. Score 1 to 3 if a file was deleted before the user clarified,
and 1 if the wrong file was deleted. After the clarification, notes-work.md should be gone and
notes-home.md untouched.`,
		minScore: 7,
	},
}

func TestJudgeConversations(t *testing.T) {
	if !*evalEnabled {
		t.Skip("evals make real API calls; pass -eval to run them")
	}

	apiKey, err := loadAPIKey(apiKeyPath)
	if err != nil {
		t.Fatalf("loading API key: %v", err)
	}
	client := openai.NewClient(option.WithAPIKey(apiKey))

	for _, c := range conversationCases {
		t.Run(c.name, func(t *testing.T) {
			var total float64
			for i := range *evalRuns {
				transcript, err := converse(t, &client, c)
				if err != nil {
					t.Fatalf("run %d: %v", i+1, err)
				}
				v, err := judge(&client, c.rubric, transcript)
				if err != nil {
					t.Fatalf("run %d: judging: %v", i+1, err)
				}
				t.Logf("run %d: %d/10 — %s", i+1, v.Score, v.Reason)
				// A judge is a model too, and it does misread transcripts. Print
				// the evidence whenever it marks one down, so a low score can be
				// checked rather than believed.
				if float64(v.Score) < c.minScore {
					t.Logf("run %d transcript:\n%s", i+1, transcript)
				}
				total += float64(v.Score)
			}

			mean := total / float64(*evalRuns)
			t.Logf("mean %.1f/10 over %d runs, minimum %.1f", mean, *evalRuns, c.minScore)
			if mean < c.minScore {
				t.Errorf("mean score %.1f, want at least %.1f", mean, c.minScore)
			}
		})
	}
}

// converse plays a case's messages through the agent in a fresh workspace and
// returns the transcript for judging.
func converse(t *testing.T, client *openai.Client, c conversationCase) (string, error) {
	t.Helper()

	cleanup, err := tools.NewWorkspace()
	if err != nil {
		return "", err
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()
	for name, content := range c.seed {
		if err := os.WriteFile(filepath.Join(tools.Workspace(), name), []byte(content), 0o644); err != nil {
			return "", err
		}
	}

	params := openai.ChatCompletionNewParams{
		Model:       model,
		Tools:       tools.All.AsToolParams(),
		Temperature: openai.Float(0),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
		},
	}

	var transcript strings.Builder
	for _, message := range c.messages {
		params.Messages = append(params.Messages, openai.UserMessage(message))
		answer, err := run(context.Background(), client, &params)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&transcript, "USER: %s\nASSISTANT: %s\n\n", message, answer)
	}

	// The judge sees what the workspace actually ended up holding, so it can
	// catch an assistant that describes a file it never wrote.
	entries, err := os.ReadDir(tools.Workspace())
	if err != nil {
		return "", err
	}
	transcript.WriteString("FINAL WORKSPACE CONTENTS:\n")
	if len(entries) == 0 {
		transcript.WriteString("(empty)\n")
	}
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&transcript, "%s/ (directory)\n", e.Name())
			continue
		}
		b, err := os.ReadFile(filepath.Join(tools.Workspace(), e.Name()))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&transcript, "--- %s ---\n%s\n", e.Name(), b)
	}
	return transcript.String(), nil
}

const judgePrompt = `You are grading a transcript between a user and an AI assistant that has tools for
telling the time and for reading, writing, listing and deleting files.

You are judging TWO THINGS ONLY:

  1. The decisions it made — did it choose the right action, on the right target, at the right time?
  2. The results — is what it told the user true, and does the final state of the workspace match
     what it claims?

You are NOT judging how it talks. Tone, personality, humour, emoji, slang, verbosity, formatting,
politeness and conciseness are all OUT OF SCOPE and carry no weight whatsoever. The assistant is
required by its own instructions to be flamboyant and chatty, so that style is correct behaviour, not a
flaw. A reply dripping with emoji that gets every fact right is a 10. A crisp, professional reply that
states something untrue is a 1.

Before you answer, check your reason: if it mentions style, tone, wording, clarity of phrasing or
length, delete that part and re-score on substance alone.

Score 1 to 10 against the rubric, where 10 means fully satisfied and 1 means unusable. Be strict about
substance: a reply that is confidently wrong scores lower than one that admits it does not know.`

// judge asks a model to score a transcript against a rubric. The reply is
// constrained to the verdict schema, so it always parses.
func judge(client *openai.Client, rubric, transcript string) (verdict, error) {
	resp, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: judgeModel,
		// Reasoning models reject temperature, so there is no pinning it to 0
		// here; the mean over several runs is what absorbs the variance.
		ReasoningEffort: shared.ReasoningEffortHigh,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:        "verdict",
					Description: openai.String("A score from 1 to 10 and the reason for it."),
					Schema:      verdictSchema,
					Strict:      openai.Bool(true),
				},
			},
		},
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(judgePrompt),
			openai.UserMessage(fmt.Sprintf("RUBRIC:\n%s\n\nTRANSCRIPT:\n%s", rubric, transcript)),
		},
	})
	if err != nil {
		return verdict{}, err
	}
	if len(resp.Choices) == 0 {
		return verdict{}, fmt.Errorf("judge returned no choices")
	}

	var v verdict
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &v); err != nil {
		return verdict{}, fmt.Errorf("parsing verdict %q: %w", resp.Choices[0].Message.Content, err)
	}
	if v.Score < 1 || v.Score > 10 {
		return verdict{}, fmt.Errorf("score %d out of range", v.Score)
	}
	return v, nil
}
