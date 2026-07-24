package id_test

import (
	stderrors "errors"
	"sync"
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

// errReader always fails — it stands in for an exhausted or broken entropy source so
// we can prove New surfaces the failure as an error rather than panicking (ADR-0006).
type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, stderrors.New("entropy source unavailable")
}

var _ = Describe("Generator", func() {
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	newGen := func() *id.Generator {
		return id.NewGenerator(clock.NewFake(t0), constReader{b: 0x42})
	}

	It("mints a prefixed id that round-trips through Parse", func() {
		s, err := newGen().New("run")
		Expect(err).NotTo(HaveOccurred())
		Expect(s).To(HavePrefix("run_"))

		_, err = id.Parse("run", s)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects an id parsed under the wrong prefix", func() {
		s, err := newGen().New("run") // a run id...
		Expect(err).NotTo(HaveOccurred())
		_, err = id.Parse("tkt", s) // ...parsed as a ticket id
		Expect(err).To(HaveOccurred())
	})

	It("sorts chronologically by creation time", func() {
		early := id.NewGenerator(clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), constReader{})
		late := id.NewGenerator(clock.NewFake(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)), constReader{})

		earlyID, err := early.New("run")
		Expect(err).NotTo(HaveOccurred())
		lateID, err := late.New("run")
		Expect(err).NotTo(HaveOccurred())

		Expect(earlyID < lateID).To(BeTrue(), "lexical order should equal creation order")
	})

	It("produces identical ids from identical clock and entropy", func() {
		a, err := newGen().New("run")
		Expect(err).NotTo(HaveOccurred())
		b, err := newGen().New("run")
		Expect(err).NotTo(HaveOccurred())
		Expect(a).To(Equal(b))
	})

	It("returns a wrapped error instead of panicking when entropy fails", func() {
		g := id.NewGenerator(clock.NewFake(t0), errReader{})
		s, err := g.New("run")
		Expect(err).To(HaveOccurred())
		Expect(s).To(BeEmpty())
		Expect(err.Error()).To(ContainSubstring(`mint "run" id`))
	})

	It("mints concurrently without a data race", func() {
		g := id.NewGenerator(clock.NewFake(t0), constReader{b: 0x42})
		const goroutines = 32

		var wg sync.WaitGroup
		errs := make(chan error, goroutines)
		for range goroutines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := g.New("run"); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)

		for err := range errs {
			Expect(err).NotTo(HaveOccurred())
		}
	})
})
