package temporal

import (
	"testing"

	"go.temporal.io/sdk/worker"
)

// TestWorkflowReplayer_RegisterWorkflow verifies the workflow can be registered
// with the Temporal WorkflowReplayer. This is a lightweight, non-replay test
// that guards the registration surface and ensures the workflow signature is
// compatible with the replayer.
func TestWorkflowReplayer_RegisterWorkflow(t *testing.T) {
	r := worker.NewWorkflowReplayer()
	// Registering should not panic.
	r.RegisterWorkflow(AgentWorkflow)
}

// TestWorkflowReplayer_ReplayHistory is a placeholder for a full replay test
// using a recorded history JSON. Add a real history file under
// temporal/testdata/ and remove t.Skip to enable.
func TestWorkflowReplayer_ReplayHistory(t *testing.T) {
	t.Skip("requires a recorded Temporal workflow history in temporal/testdata/")
}
