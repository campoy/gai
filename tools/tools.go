// Package tools contains the utility functions and tool registry used by the agent.
package tools

import (
	"context"
	"errors"

	"github.com/openai/openai-go"
)

// ErrInvalidArgument marks a tool failure caused by the arguments the model
// sent: JSON that does not parse, a missing path, a path outside the
// workspace. Running the same call again cannot turn it into a success — only
// the model calling differently can — which is what separates it from a
// failure worth retrying, like a search whose model call was rate limited.
//
// Match it with errors.Is. The local agent loop makes no use of the
// distinction and hands every tool error back to the model as text; the
// Temporal path uses it to fail the activity without spending its retry
// budget first.
var ErrInvalidArgument = errors.New("invalid argument")

// Tool represents a utility function with a name, description, and the function itself.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON Schema describing the function's arguments.
	// A nil value declares a function that takes no arguments.
	Parameters map[string]any
	Func       Function
}

// Function defines the signature for utility functions that take the call's
// arguments as JSON and return a string output along with an error.
//
// The context is the one the agent loop is running under, carrying both its
// deadline and the span of the tool call. A tool that does I/O of its own is
// expected to pass it on: without it the work is uncancellable, and any span it
// starts is a root rather than a child of the call that caused it. Tools that
// only touch local state ignore it.
type Function func(ctx context.Context, args string) (string, error)

// New creates a new Tool instance with the provided name, description, parameter schema, and function.
func New(name, description string, parameters map[string]any, fn Function) Tool {
	return Tool{
		Name:        name,
		Description: description,
		Parameters:  parameters,
		Func:        fn,
	}
}

// AsToolParam converts the tool into the schema advertised to the model. Only
// the name links a schema back to its implementation.
func (t Tool) AsToolParam() openai.ChatCompletionToolParam {
	return openai.ChatCompletionToolParam{
		Function: openai.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  t.Parameters,
		},
	}
}

// Tools is a set of tools that can be advertised to the model together.
type Tools []Tool

// AsToolParams converts every tool in the set into its schema.
func (ts Tools) AsToolParams() []openai.ChatCompletionToolParam {
	params := make([]openai.ChatCompletionToolParam, len(ts))
	for i, t := range ts {
		params[i] = t.AsToolParam()
	}
	return params
}

// All returns every tool available to the agent. It takes a client because
// web_search runs a model call of its own, and a workspace because the file
// tools resolve every path against one. Both are closed over as the tools are
// built, so two sets built here are independent: two agents in one process
// never share a directory, and neither reads state the other can write.
func All(client *openai.Client, workspace Workspace) Tools {
	return Tools{
		DateTime,
		NewReadFile(workspace),
		NewWriteFile(workspace),
		NewListFiles(workspace),
		NewDeleteFile(workspace),
		NewWebSearch(client),
	}
}

// ByName returns the tool in the set registered under the given name.
func (ts Tools) ByName(name string) (Tool, bool) {
	for _, t := range ts {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
