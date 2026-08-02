package workflows

import (
	"errors"

	"github.com/0x63616c/software-factory/internal/work"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

var _ = Describe("merge activity options", func() {
	It("refuses an expired semantic window", func() {
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
		Expect(env.GetWorkflowError()).NotTo(HaveOccurred(), "merge activity options")
	})
})
