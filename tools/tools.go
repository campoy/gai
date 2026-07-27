// Tools contains utility functions for the application.
package tools

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

// All is every tool available to the agent.
var All = []Tool{DateTime}

// ByName returns the tool registered under the given name.
func ByName(name string) (Tool, bool) {
	for _, t := range All {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
