// Tools contains utility functions for the application.
package tools

import "github.com/openai/openai-go"

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
type Function func(args string) (string, error)

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

// All is every tool available to the agent.
var All = Tools{DateTime, ReadFile, WriteFile, ListFiles, DeleteFile}

// ByName returns the tool registered under the given name.
func ByName(name string) (Tool, bool) {
	for _, t := range All {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
