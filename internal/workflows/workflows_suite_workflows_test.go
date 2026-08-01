package workflows

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWorkflowsInternalSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Workflows Internal Suite")
}
