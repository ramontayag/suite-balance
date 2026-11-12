package balance

import (
	"fmt"
	"math"
	"slices"
)

const observationWindow = 10

// Sample is a complete, passing file observation from an eligible run.
// RunID orders runs by creation, not by when their results arrive.
type Sample struct {
	RunID   uint64  `json:"run_id"`
	Attempt uint64  `json:"attempt"`
	Seconds float64 `json:"duration_seconds"`
}

type Estimate struct {
	Seconds float64 `json:"duration_seconds"`
	Samples int     `json:"samples"`
}

// History retains one observation per file and run. Its zero value is ready to use.
// Callers keep environments separate and check run eligibility before recording.
type History struct {
	files map[string][]Sample
}

func (h *History) Record(file string, sample Sample) error {
	if h == nil {
		return fmt.Errorf("cannot record an observation in a nil history")
	}
	if err := validatePath(file); err != nil {
		return err
	}
	if sample.RunID == 0 || sample.Attempt == 0 {
		return fmt.Errorf("%q: run ID and attempt must be positive", file)
	}
	if sample.Seconds < 0 || math.IsNaN(sample.Seconds) || math.IsInf(sample.Seconds, 0) {
		return fmt.Errorf("%q: duration must be finite and nonnegative", file)
	}

	samples := slices.Clone(h.files[file])
	for i, previous := range samples {
		if previous.RunID != sample.RunID {
			continue
		}
		if previous.Attempt > sample.Attempt {
			return nil
		}
		if previous.Attempt == sample.Attempt {
			if previous.Seconds != sample.Seconds {
				return fmt.Errorf("%q: conflicting observations for run %d attempt %d", file, sample.RunID, sample.Attempt)
			}
			return nil
		}
		samples = slices.Delete(samples, i, i+1)
		break
	}
	samples = append(samples, sample)
	slices.SortFunc(samples, func(a, b Sample) int {
		if a.RunID < b.RunID {
			return -1
		}
		if a.RunID > b.RunID {
			return 1
		}
		return 0
	})
	if len(samples) > observationWindow {
		samples = slices.Clone(samples[len(samples)-observationWindow:])
	}
	if h.files == nil {
		h.files = make(map[string][]Sample)
	}
	h.files[file] = samples
	return nil
}

func (h *History) Estimate(file string) (Estimate, bool) {
	if h == nil || len(h.files[file]) == 0 {
		return Estimate{}, false
	}
	samples := h.files[file]
	mean := 0.0
	for i, sample := range samples {
		mean += (sample.Seconds - mean) / float64(i+1)
	}
	return Estimate{Seconds: mean, Samples: len(samples)}, true
}

func (h *History) Samples(file string) []Sample {
	if h == nil {
		return nil
	}
	return slices.Clone(h.files[file])
}
