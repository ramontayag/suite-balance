package balance

import (
	"fmt"
	"io"
	"math"
	"slices"
)

type FileResult struct {
	Seconds float64 `json:"duration_seconds"`
	Status  string  `json:"status"`
}

type Report struct {
	Version int                   `json:"version"`
	Files   map[string]FileResult `json:"files"`
}

type wireResult struct {
	Seconds *float64 `json:"duration_seconds"`
	Status  string   `json:"status"`
}

type wireReport struct {
	Version int                   `json:"version"`
	Files   map[string]wireResult `json:"files"`
}

// ReadReport rejects ambiguous results instead of silently accepting a second
// occurrence of the same file or treating a missing duration as zero.
func ReadReport(reader io.Reader) (Report, error) {
	var input wireReport
	if err := decodeStrict(reader, &input); err != nil {
		return Report{}, fmt.Errorf("invalid timing report: %w", err)
	}
	return input.report()
}

func (input wireReport) report() (Report, error) {
	if input.Version != 1 || input.Files == nil {
		return Report{}, fmt.Errorf("timing report requires version 1 and a files object")
	}
	report := Report{Version: 1, Files: make(map[string]FileResult, len(input.Files))}
	for file, result := range input.Files {
		if err := validatePath(file); err != nil {
			return Report{}, err
		}
		if result.Seconds == nil || *result.Seconds < 0 || math.IsNaN(*result.Seconds) || math.IsInf(*result.Seconds, 0) {
			return Report{}, fmt.Errorf("%q: duration_seconds must be finite and nonnegative", file)
		}
		if result.Status != "passed" && result.Status != "failed" {
			return Report{}, fmt.Errorf("%q: status must be passed or failed", file)
		}
		report.Files[file] = FileResult{Seconds: *result.Seconds, Status: result.Status}
	}
	return report, nil
}

func (r Report) CheckFiles(assigned []string) error {
	seen := make(map[string]bool, len(assigned))
	for _, file := range assigned {
		if seen[file] {
			return fmt.Errorf("assigned file %q appears more than once", file)
		}
		seen[file] = true
		if _, ok := r.Files[file]; !ok {
			return fmt.Errorf("assigned file %q has no complete result", file)
		}
	}
	for file := range r.Files {
		if !seen[file] {
			return fmt.Errorf("reported file %q is not assigned to this shard", file)
		}
	}
	return nil
}

func (r Report) PassingFiles() []string {
	files := []string{}
	for file, result := range r.Files {
		if result.Status == "passed" {
			files = append(files, file)
		}
	}
	slices.Sort(files)
	return files
}
