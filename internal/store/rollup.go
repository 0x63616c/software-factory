package store

import "github.com/0x63616c/software-factory/internal/work"

// Usage rolls up every Attempt of this Step into one total, and reports
// whether that total is complete.
//
// complete is false when any Attempt is not Measured — a resumed Attempt
// ran nothing and has no usage to add, so its absence from the sum must be
// visible rather than read as a confident zero (ADR-0012, "the one thing
// that must not be got wrong"). The caller renders "unknown", never 0, for
// an incomplete total.
func (d StepDetail) Usage() (usage work.Usage, complete bool) {
	complete = true
	for _, attempt := range d.Attempts {
		if !attempt.Measured {
			complete = false
			continue
		}
		usage = usage.Add(attempt.Usage)
	}
	return usage, complete
}

// Usage rolls up every Step of this Run into one total. It is incomplete
// under the same rule Step.Usage documents: any unmeasured Attempt anywhere
// in the Run makes the Run's own total incomplete, because Usage.Add cannot
// recover a Step's missing part afterwards.
func (d RunDetail) Usage() (usage work.Usage, complete bool) {
	complete = true
	for _, step := range d.Steps {
		stepUsage, stepComplete := step.Usage()
		usage = usage.Add(stepUsage)
		if !stepComplete {
			complete = false
		}
	}
	return usage, complete
}
