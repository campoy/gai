package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
)

// searchModel answers the query. OpenAI's search-preview models run the search
// themselves and return prose with citations attached, so this tool is a
// wrapper around a second model call rather than a search API client.
//
// These models cannot be the agent itself: they do not support function
// calling, which is why the search lives behind a tool instead of being turned
// on for the main loop.
const searchModel = openai.ChatModelGPT4oMiniSearchPreview

// NewWebSearch returns a tool that answers questions using OpenAI's hosted web
// search. It needs a client of its own because the search is a second model
// call; the agent's client is the natural one to hand it, since the search runs
// on the same account.
func NewWebSearch(client *openai.Client) Tool {
	return New(
		"web_search",
		"Search the web and get an answer with sources. Use for anything that happened recently, anything that changes over time, and anything you are unsure of — your training data has a cutoff and this does not.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "What to look up, phrased as a question or search query.",
				},
			},
			"required": []string{"query"},
		},
		func(args string) (string, error) { return webSearch(client, args) },
	)
}

func webSearch(client *openai.Client, args string) (string, error) {
	var p struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Query) == "" {
		return "", fmt.Errorf("query is required")
	}

	resp, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: searchModel,
		// An empty options value is still what turns the search on: the field is
		// omitzero, so leaving it out sends no web_search_options at all.
		WebSearchOptions: openai.ChatCompletionNewParamsWebSearchOptions{},
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(p.Query),
		},
	})
	if err != nil {
		return "", fmt.Errorf("searching: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("search returned no choices")
	}

	msg := resp.Choices[0].Message
	var b strings.Builder
	b.WriteString(msg.Content)

	// Pass the citations through so the agent can attribute what it repeats,
	// and so a caller can tell a searched claim from a remembered one.
	if len(msg.Annotations) > 0 {
		b.WriteString("\n\nSources:\n")
		seen := map[string]bool{}
		for _, a := range msg.Annotations {
			c := a.URLCitation
			if c.URL == "" || seen[c.URL] {
				continue
			}
			seen[c.URL] = true
			fmt.Fprintf(&b, "- %s: %s\n", c.Title, c.URL)
		}
	}
	return b.String(), nil
}
