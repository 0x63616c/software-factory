package clock_test

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/0x63616c/software-factory/internal/clock"
)

func TestClock(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Clock Suite")
}

var _ = Describe("Fake", func() {
	// A fixed instant — never time.Now(); tests must be deterministic.
	start := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	It("reports its pinned instant", func() {
		c := clock.NewFake(start)
		Expect(c.Now()).To(Equal(start))
	})

	It("moves only when advanced", func() {
		c := clock.NewFake(start)
		c.Advance(90 * time.Minute)
		Expect(c.Now()).To(Equal(start.Add(90 * time.Minute)))
	})
})

var _ = Describe("System", func() {
	It("satisfies the Clock interface", func() {
		var _ clock.Clock = clock.System{}
	})
})
