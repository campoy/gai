// Package temporal contains the Temporal workflow and activity scaffold used by the CLI.
package temporal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/campoy/gai/agent"
	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	// DefaultHostPort is the default Temporal endpoint used by the local CLI worker and client.
	DefaultHostPort = "127.0.0.1:7233"
	// DefaultTaskQueue is the task queue used when the CLI does not override it.
	DefaultTaskQueue = "gai"
)

const (
	maxWorkflowSteps = 10

	activityStartToCloseTimeout = 5 * time.Minute
)

const apiKeyEnvName = "GAI_API_KEY"

// Error types reported for activity failures a retry cannot fix. Temporal
// records the type in Event History and shows it in the UI, so these names are
// what a failed run is read back by; they are also what would go in
// RetryPolicy.NonRetryableErrorTypes if the failure were ever raised somewhere
// that cannot reach for NewNonRetryableApplicationError.
const (
	errTypeMissingAPIKey   = "MissingAPIKey"
	errTypeNoChoices       = "NoChoicesReturned"
	errTypeUnknownTool     = "UnknownTool"
	errTypeInvalidToolArgs = "InvalidToolArguments"
)

// defaultActivityRetryPolicy covers failures that a second attempt might
// survive: a dropped connection, a rate limit, a worker that died mid-call.
// Anything permanent — no key, a tool that does not exist, arguments that do
// not parse — is returned as a non-retryable application error instead, so it
// fails on the first attempt rather than sleeping through the backoff first.
var defaultActivityRetryPolicy = &temporal.RetryPolicy{
	InitialInterval:    time.Second,
	BackoffCoefficient: 2.0,
	MaximumInterval:    30 * time.Second,
	MaximumAttempts:    3,
}

func defaultActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: activityStartToCloseTimeout,
		RetryPolicy:         defaultActivityRetryPolicy,
	}
}

// CompletionRequest is the payload used to ask the chat completion activity
// for a single agent turn. It carries no workspace: the completion call needs
// the tool schemas, which are the same wherever the run happens, and never
// touches a file itself.
type CompletionRequest struct {
	Messages []openai.ChatCompletionMessageParamUnion `json:"messages"`
}

// CompletionResult is the activity response returned by the chat completion
// activity. It carries the assistant message (as a param union) and token
// usage so the workflow can make compaction decisions if needed.
type CompletionResult struct {
	Message *openai.ChatCompletionMessageParamUnion `json:"message"`
	Usage   int64                                   `json:"usage"`
}

// ToolInvocation is the payload used to run a single tool activity. The
// WorkspaceDir field lets the caller request the tool execute in the same
// ephemeral directory used by the overall run. It is opened per invocation and
// closed over by that invocation's tool set, so two activities running side by
// side on one worker cannot resolve paths against each other's directory.
type ToolInvocation struct {
	Name         string `json:"name"`
	Arguments    string `json:"arguments"`
	WorkspaceDir string `json:"workspace_dir"`
}

// NewClient creates a Temporal client for the configured host and port.
func NewClient(hostPort string) (client.Client, error) {
	if hostPort == "" {
		hostPort = os.Getenv("TEMPORAL_HOST_PORT")
	}
	if hostPort == "" {
		hostPort = DefaultHostPort
	}
	return client.Dial(client.Options{HostPort: hostPort})
}

// NewWorker creates a Temporal worker for the configured task queue.
func NewWorker(hostPort, taskQueue string) (worker.Worker, error) {
	c, err := NewClient(hostPort)
	if err != nil {
		return nil, err
	}
	if taskQueue == "" {
		taskQueue = DefaultTaskQueue
	}
	return worker.New(c, taskQueue, worker.Options{}), nil
}

// Register wires the workflow and activities into a Temporal worker.
func Register(w worker.Worker) {
	w.RegisterWorkflow(AgentWorkflow)
	w.RegisterActivity(ChatCompletionActivity)
	w.RegisterActivity(RunToolActivity)
}

// ConfigureAPIKey stores the API key used by the Temporal worker activities.
func ConfigureAPIKey(key string) error {
	if key == "" {
		return fmt.Errorf("api key is required")
	}
	return os.Setenv(apiKeyEnvName, key)
}

// APIKey returns the API key currently configured for the worker activities.
func APIKey() string {
	return os.Getenv(apiKeyEnvName)
}

// AgentWorkflow drives the agent loop through Temporal activities.
func AgentWorkflow(ctx workflow.Context, prompt, workspaceDir string) (string, error) {
	activityCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions())

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(agent.SystemPrompt),
		openai.UserMessage(prompt),
	}

	for range maxWorkflowSteps {
		var result *CompletionResult
		err := workflow.ExecuteActivity(activityCtx, ChatCompletionActivity, CompletionRequest{Messages: messages}).Get(ctx, &result)
		if err != nil {
			return "", err
		}
		if result == nil || result.Message == nil {
			return "", fmt.Errorf("chat completion activity returned no message")
		}
		messages = append(messages, *result.Message)

		assistant := result.Message.OfAssistant
		if assistant == nil {
			return "", fmt.Errorf("expected assistant message")
		}
		if len(assistant.ToolCalls) == 0 {
			return assistant.Content.OfString.Or(""), nil
		}

		for _, tc := range assistant.ToolCalls {
			var output string
			err := workflow.ExecuteActivity(activityCtx, RunToolActivity, ToolInvocation{Name: tc.Function.Name, Arguments: tc.Function.Arguments, WorkspaceDir: workspaceDir}).Get(ctx, &output)
			if err != nil {
				messages = append(messages, openai.ToolMessage("error: "+err.Error(), tc.ID))
				continue
			}
			messages = append(messages, openai.ToolMessage(output, tc.ID))
		}
	}

	return "", fmt.Errorf("gave up after %d steps without a final answer", maxWorkflowSteps)
}

// ChatCompletionActivity calls the OpenAI chat completions API from a Temporal activity.
func ChatCompletionActivity(ctx context.Context, req CompletionRequest) (*CompletionResult, error) {
	key := APIKey()
	if key == "" {
		return nil, temporal.NewNonRetryableApplicationError("missing API key", errTypeMissingAPIKey, nil)
	}

	// This activity needs the tool schemas, not the tools: it puts them on the
	// request and never dispatches one — RunToolActivity does that, in the
	// run's own directory. An agent is built whole, though, so it gets a
	// throwaway workspace of its own that is deleted on the way out. Nothing is
	// ever written into it.
	//
	// It used to call SetWorkspace(req.WorkspaceDir) instead, which reached
	// into the run's directory to no purpose and widened the window in which
	// two activities on one worker overwrote each other's.
	workspace, cleanup, err := tools.NewWorkspace()
	if err != nil {
		return nil, err
	}
	defer func() { _ = cleanup() }()

	client := openai.NewClient(option.WithAPIKey(key))
	ag := agent.New(&client, workspace)
	params := ag.Params()
	params.Messages = req.Messages

	resp, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, temporal.NewNonRetryableApplicationError("no choices returned", errTypeNoChoices, nil)
	}

	msg := resp.Choices[0].Message.ToParam()
	return &CompletionResult{Message: &msg, Usage: resp.Usage.TotalTokens}, nil
}

// RunToolActivity invokes a registered tool from a Temporal activity.
func RunToolActivity(ctx context.Context, req ToolInvocation) (string, error) {
	key := APIKey()
	if key == "" {
		return "", temporal.NewNonRetryableApplicationError("missing API key", errTypeMissingAPIKey, nil)
	}
	workspace, err := tools.OpenWorkspace(req.WorkspaceDir)
	if err != nil {
		return "", err
	}

	client := openai.NewClient(option.WithAPIKey(key))
	// Built per invocation, around this call's own workspace, so concurrent
	// activities on one worker never share a directory.
	toolsSet := tools.All(&client, workspace)
	tool, ok := toolsSet.ByName(req.Name)
	if !ok {
		// A name the model invented. Retrying invents nothing new; the workflow
		// turns this into a tool message and the model gets to correct itself.
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("unknown tool %s", req.Name), errTypeUnknownTool, nil)
	}

	out, err := tool.Func(ctx, req.Arguments)
	// Arguments the model got wrong fail the same way on every attempt. Other
	// tool failures — a search whose model call was rate limited, a read that
	// hit a transient I/O error — keep the retry policy.
	if errors.Is(err, tools.ErrInvalidArgument) {
		return "", temporal.NewNonRetryableApplicationError(err.Error(), errTypeInvalidToolArgs, err)
	}
	return out, err
}
