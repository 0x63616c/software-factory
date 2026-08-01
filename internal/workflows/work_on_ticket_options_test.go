package workflows

import (
	"errors"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestMergeActivityOptionsRefuseAnExpiredSemanticWindow(t *testing.T) {
	t.Parallel()
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		expired := workflow.WithValue(ctx, semanticDeadlineContextKey{}, workflow.Now(ctx))
		_, ready := mergeActivityOptions(expired, work.DefaultTargetRunPolicy().Merge)
		if ready {
			return errors.New("expired semantic window produced merge activity options")
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("merge activity options: %v", err)
	}
}
