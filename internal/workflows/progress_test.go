package workflows

import (
	"github.com/0x63616c/software-factory/internal/work"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("progress helpers", func() {
	DescribeTable("sameCheckFailures compares sets independent of order",
		func(tc struct {
			name string
			a, b []work.CheckFailure
			want bool
		}) {
			Expect(sameCheckFailures(tc.a, tc.b)).To(Equal(tc.want), "sameCheckFailures(%v, %v)", tc.a, tc.b)
		},
		Entry("empty sets match", struct {
			name string
			a, b []work.CheckFailure
			want bool
		}{
			name: "empty sets match",
			a:    nil,
			b:    nil,
			want: true,
		}),
		Entry("identical sets match despite order", struct {
			name string
			a, b []work.CheckFailure
			want bool
		}{
			name: "identical sets match despite order",
			a:    []work.CheckFailure{{Name: "a", Fingerprint: "one"}, {Name: "b", Fingerprint: "two"}},
			b:    []work.CheckFailure{{Name: "b", Fingerprint: "two"}, {Name: "a", Fingerprint: "one"}},
			want: true,
		}),
		Entry("strict subset does not match", struct {
			name string
			a, b []work.CheckFailure
			want bool
		}{
			name: "strict subset does not match",
			a:    []work.CheckFailure{{Name: "a", Fingerprint: "one"}},
			b:    []work.CheckFailure{{Name: "a", Fingerprint: "one"}, {Name: "b", Fingerprint: "two"}},
			want: false,
		}),
		Entry("added failure does not match", struct {
			name string
			a, b []work.CheckFailure
			want bool
		}{
			name: "added failure does not match",
			a:    []work.CheckFailure{{Name: "a", Fingerprint: "one"}, {Name: "b", Fingerprint: "two"}},
			b:    []work.CheckFailure{{Name: "a", Fingerprint: "one"}},
			want: false,
		}),
		Entry("same check name with another failure does not match", struct {
			name string
			a, b []work.CheckFailure
			want bool
		}{
			name: "same check name with another failure does not match",
			a:    []work.CheckFailure{{Name: "test", Fingerprint: "one"}},
			b:    []work.CheckFailure{{Name: "test", Fingerprint: "two"}},
			want: false,
		}),
		Entry("evidence does not change an otherwise identical failure", struct {
			name string
			a, b []work.CheckFailure
			want bool
		}{
			name: "evidence does not change an otherwise identical failure",
			a:    []work.CheckFailure{{Name: "test", Fingerprint: "one", Evidence: "first bounded log"}},
			b:    []work.CheckFailure{{Name: "test", Fingerprint: "one", Evidence: "second bounded log"}},
			want: true,
		}),
		Entry("missing fingerprints never claim stagnation", struct {
			name string
			a, b []work.CheckFailure
			want bool
		}{
			name: "missing fingerprints never claim stagnation",
			a:    []work.CheckFailure{{Name: "test"}},
			b:    []work.CheckFailure{{Name: "test"}},
			want: false,
		}))

	DescribeTable("intersects reports overlap between sets",
		func(tc struct {
			name string
			a, b []string
			want bool
		}) {
			Expect(intersects(tc.a, tc.b)).To(Equal(tc.want), "intersects(%v, %v)", tc.a, tc.b)
		},
		Entry("a shared id", struct {
			name string
			a, b []string
			want bool
		}{
			name: "a shared id",
			a:    []string{"x", "y"},
			b:    []string{"y", "z"},
			want: true,
		}),
		Entry("disjoint sets", struct {
			name string
			a, b []string
			want bool
		}{
			name: "disjoint sets",
			a:    []string{"x"},
			b:    []string{"y"},
			want: false,
		}),
		Entry("a is empty", struct {
			name string
			a, b []string
			want bool
		}{
			name: "a is empty",
			a:    nil,
			b:    []string{"y"},
			want: false,
		}),
		Entry("b is empty", struct {
			name string
			a, b []string
			want bool
		}{
			name: "b is empty",
			a:    []string{"x"},
			b:    nil,
			want: false,
		}),
		Entry("both empty", struct {
			name string
			a, b []string
			want bool
		}{
			name: "both empty",
			a:    nil,
			b:    nil,
			want: false,
		}))
})
