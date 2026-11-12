package balance

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

type Source struct {
	Repository    string `json:"repository"`
	Workflow      string `json:"workflow"`
	RunID         uint64 `json:"run_id"`
	Attempt       uint64 `json:"attempt"`
	Commit        string `json:"commit"`
	Environment   string `json:"environment"`
	Coverage      string `json:"coverage"`
	Event         string `json:"event"`
	Branch        string `json:"branch"`
	DefaultBranch string `json:"default_branch"`
}

func (s Source) validate() error {
	if s.RunID == 0 || s.Attempt == 0 || !validCommit(s.Commit) || s.Repository == "" || s.Workflow == "" || s.Branch == "" || s.DefaultBranch == "" || s.Event == "" {
		return fmt.Errorf("source requires repository, workflow, positive run/attempt, commit, event, branch, and default branch")
	}
	if !printable(s.Environment) || (s.Coverage != "full" && s.Coverage != "selected") {
		return fmt.Errorf("source requires a printable environment and full or selected coverage")
	}
	return nil
}

func (s Source) eligible() bool {
	return s.Coverage == "full" && s.Event == "push" && s.Branch == s.DefaultBranch
}

func ReadSource(reader io.Reader) (Source, error) {
	var source Source
	if err := decodeStrict(reader, &source); err != nil {
		return Source{}, err
	}
	return source, source.validate()
}

type Observation struct {
	Version int                   `json:"version"`
	Source  Source                `json:"source"`
	Files   map[string]FileResult `json:"files"`
}

func ReadObservation(reader io.Reader) (Observation, error) {
	var input struct {
		Version int                   `json:"version"`
		Source  Source                `json:"source"`
		Files   map[string]wireResult `json:"files"`
	}
	if err := decodeStrict(reader, &input); err != nil {
		return Observation{}, err
	}
	report, err := (wireReport{Version: input.Version, Files: input.Files}).report()
	if err != nil {
		return Observation{}, err
	}
	observation := Observation{Version: input.Version, Source: input.Source, Files: report.Files}
	return observation, observation.validate()
}

func (o Observation) validate() error {
	if o.Version != 1 || len(o.Files) == 0 {
		return fmt.Errorf("observation requires version 1 and at least one passing file")
	}
	if err := o.Source.validate(); err != nil {
		return err
	}
	if !o.Source.eligible() {
		return fmt.Errorf("observation is not from a full-suite default-branch push")
	}
	for file, result := range o.Files {
		if result.Status != "passed" {
			return fmt.Errorf("observation contains non-passing file %q", file)
		}
		if err := (&History{}).Record(file, Sample{o.Source.RunID, o.Source.Attempt, result.Seconds}); err != nil {
			return err
		}
	}
	return nil
}

type HistorySource struct {
	RunID   uint64 `json:"run_id"`
	Attempt uint64 `json:"attempt"`
	Commit  string `json:"commit"`
}

// BuildHistory reconstructs bounded windows from immutable run observations.
// Artifact authentication belongs to the caller, not to the JSON payload.
func BuildHistory(observations []Observation, environment string) (*History, []HistorySource, error) {
	history := &History{}
	sources := []HistorySource{}
	seen := map[string]string{}
	commits := map[uint64]string{}
	var identity Source
	for _, observation := range observations {
		if err := observation.validate(); err != nil {
			return nil, nil, err
		}
		source := observation.Source
		if source.Environment != environment {
			return nil, nil, fmt.Errorf("stored runner environment does not match this run")
		}
		if identity.Repository != "" && (source.Repository != identity.Repository || source.Workflow != identity.Workflow || source.DefaultBranch != identity.DefaultBranch) {
			return nil, nil, fmt.Errorf("history contains incompatible repository, workflow, or default branch")
		}
		identity = source
		if previous, ok := commits[source.RunID]; ok && previous != source.Commit {
			return nil, nil, fmt.Errorf("run %d has conflicting tested commits", source.RunID)
		}
		commits[source.RunID] = source.Commit
		key := fmt.Sprintf("%d/%d", source.RunID, source.Attempt)
		data, _ := json.Marshal(observation)
		if previous, ok := seen[key]; ok {
			if previous != string(data) {
				return nil, nil, fmt.Errorf("conflicting observations for run/attempt %s", key)
			}
			continue
		}
		seen[key] = string(data)
		sources = append(sources, HistorySource{source.RunID, source.Attempt, source.Commit})
		for file, result := range observation.Files {
			if err := history.Record(file, Sample{source.RunID, source.Attempt, result.Seconds}); err != nil {
				return nil, nil, err
			}
		}
	}
	used := map[string]bool{}
	for _, samples := range history.files {
		for _, sample := range samples {
			used[fmt.Sprintf("%d/%d", sample.RunID, sample.Attempt)] = true
		}
	}
	sources = slices.DeleteFunc(sources, func(source HistorySource) bool {
		return !used[fmt.Sprintf("%d/%d", source.RunID, source.Attempt)]
	})
	slices.SortFunc(sources, func(a, b HistorySource) int {
		if a.RunID < b.RunID || (a.RunID == b.RunID && a.Attempt < b.Attempt) {
			return -1
		}
		if a == b {
			return 0
		}
		return 1
	})
	return history, sources, nil
}

func validCommit(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	_, err := hex.DecodeString(commit)
	return err == nil
}

func printable(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsFunc(value, func(r rune) bool { return r < 32 || r == 127 })
}
