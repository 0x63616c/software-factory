package id_test

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/id"
)

func TestID(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ID Suite")
}

// constReader yields a fixed byte stream so ULID entropy is reproducible in tests —
// the injected-randomness payoff (SoftwareStyle testability floor).
type constReader struct{ b byte }

func (r constReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

var _ = Describe("Generator", func() {
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	newGen := func() *id.Generator {
		return id.NewGenerator(clock.NewFake(t0), constReader{b: 0x42})
	}

	It("mints a prefixed id that round-trips through Parse", func() {
		s := newGen().New("run")
		Expect(s).To(HavePrefix("run_"))

		_, err := id.Parse("run", s)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects an id parsed under the wrong prefix", func() {
		s := newGen().New("run")     // a run id...
		_, err := id.Parse("tkt", s) // ...parsed as a ticket id
		Expect(err).To(HaveOccurred())
	})

	It("sorts chronologically by creation time", func() {
		early := id.NewGenerator(clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), constReader{})
		late := id.NewGenerator(clock.NewFake(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)), constReader{})

		Expect(early.New("run") < late.New("run")).To(BeTrue(), "lexical order should equal creation order")
	})

	It("produces identical ids from identical clock and entropy", func() {
		Expect(newGen().New("run")).To(Equal(newGen().New("run")))
	})
})
