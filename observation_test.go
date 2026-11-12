package balance

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func source() Source {
	return Source{Repository: "owner/repo", Workflow: ".github/workflows/test.yml", RunID: 123, Attempt: 1, Commit: testCommit, Environment: "linux-standard", Coverage: "full", Event: "push", Branch: "main", DefaultBranch: "main"}
}

func observation() Observation {
	return Observation{Version: 1, Source: source(), Files: map[string]FileResult{"test/a": {Seconds: 10, Status: "passed"}}}
}

func TestObservationRoundTripPreservesSamplesAndSource(t *testing.T) {
	input := observation()
	data, _ := json.Marshal(input)
	output, err := ReadObservation(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	history, sources, err := BuildHistory([]Observation{output}, "linux-standard")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := history.Estimate("test/a"); !ok || got.Seconds != 10 || got.Samples != 1 || len(sources) != 1 || sources[0].RunID != 123 {
		t.Fatalf("observation lost data: %+v, %+v", got, sources)
	}
}

func TestHistoryReconstructionRejectsIneligibleOrIncompatibleSources(t *testing.T) {
	for name, mutate := range map[string]func(*Observation){
		"selected":          func(o *Observation) { o.Source.Coverage = "selected" },
		"pull request":      func(o *Observation) { o.Source.Event = "pull_request" },
		"branch":            func(o *Observation) { o.Source.Branch = "feature" },
		"environment":       func(o *Observation) { o.Source.Environment = "different" },
		"bad version":       func(o *Observation) { o.Version = 2 },
		"failed file":       func(o *Observation) { o.Files["test/a"] = FileResult{10, "failed"} },
		"negative duration": func(o *Observation) { o.Files["test/a"] = FileResult{-1, "passed"} },
		"infinite duration": func(o *Observation) { o.Files["test/a"] = FileResult{math.Inf(1), "passed"} },
	} {
		t.Run(name, func(t *testing.T) {
			input := observation()
			mutate(&input)
			if _, _, err := BuildHistory([]Observation{input}, "linux-standard"); err == nil {
				t.Fatal("invalid source must not enter history")
			}
		})
	}
	a, b := observation(), observation()
	b.Source.RunID++
	b.Source.Repository = "another/repo"
	if _, _, err := BuildHistory([]Observation{a, b}, "linux-standard"); err == nil {
		t.Fatal("history must not mix repositories")
	}
}

func TestHistoryReconstructionDeduplicatesAttemptsWithoutLosingOlderFiles(t *testing.T) {
	a, b := observation(), observation()
	b.Source.Attempt = 2
	b.Files = map[string]FileResult{"test/b": {Seconds: 20, Status: "passed"}}
	history, _, err := BuildHistory([]Observation{b, a, b}, "linux-standard")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := history.Estimate("test/a"); !ok || got.Seconds != 10 || got.Samples != 1 {
		t.Fatalf("retry lost a file that passed in the original attempt: %+v", got)
	}
	if got, ok := history.Estimate("test/b"); !ok || got.Seconds != 20 || got.Samples != 1 {
		t.Fatalf("retry file not retained: %+v", got)
	}
}

func TestObservationRejectsMissingDurationsAndCaseAliases(t *testing.T) {
	data, _ := json.Marshal(observation())
	for _, input := range []string{
		strings.Replace(string(data), `"duration_seconds":10,`, "", 1),
		strings.Replace(string(data), `"status":"passed"`, `"status":"passed","Status":"failed"`, 1),
		strings.Replace(string(data), `"version":1`, `"version":2`, 1),
	} {
		if _, err := ReadObservation(strings.NewReader(input)); err == nil {
			t.Fatal("invalid observation must fail")
		}
	}
}

func TestHistorySourcesOnlyIncludeRetainedObservations(t *testing.T) {
	observations := []Observation{}
	for run := uint64(1); run <= 20; run++ {
		o := observation()
		o.Source.RunID = run
		observations = append(observations, o)
	}
	_, sources, err := BuildHistory(observations, "linux-standard")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 10 || sources[0].RunID != 11 {
		t.Fatalf("source metadata should describe retained samples only: %+v", sources)
	}
}

func TestHistoryRejectsDifferentCommitsForAttemptsOfOneRun(t *testing.T) {
	a, b := observation(), observation()
	b.Source.Attempt = 2
	b.Source.Commit = strings.Repeat("b", 40)
	if _, _, err := BuildHistory([]Observation{a, b}, "linux-standard"); err == nil {
		t.Fatal("one workflow run must have one tested commit")
	}
}

func TestHistoryRejectsConflictingDocumentsForTheSameRunAttempt(t *testing.T) {
	for _, files := range []map[string]FileResult{
		{"test/a": {20, "passed"}},
		{"test/b": {10, "passed"}},
	} {
		a, b := observation(), observation()
		b.Files = files
		for _, inputs := range [][]Observation{{a, b}, {b, a}} {
			if _, _, err := BuildHistory(inputs, "linux-standard"); err == nil {
				t.Fatal("conflicting immutable observations must fail in either arrival order")
			}
		}
	}
}
