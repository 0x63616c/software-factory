package workflows_test

import (
	"github.com/0x63616c/software-factory/internal/workflows"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.temporal.io/sdk/worker"
)

var _ = Describe("replay", func() {
	It("replays exported WorkOnTicket history", func() {
		replayer := worker.NewWorkflowReplayer()
		replayer.RegisterWorkflow(workflows.WorkOnTicket)
		err := replayer.ReplayWorkflowHistoryFromJSONFile(nil, "testdata/work-on-ticket-history.json")
		Expect(err).NotTo(HaveOccurred())
	})
})
