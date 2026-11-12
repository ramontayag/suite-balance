package balance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
)

// MaxShards bounds allocations made from downloaded or user-provided input.
const MaxShards = 256

type PlanOptions struct {
	Files          []string
	Shards         int
	Coverage       string
	Environment    string
	Commit         string
	History        *History
	HistorySources []HistorySource
	FallbackReason string
}

type Plan struct {
	Version        int             `json:"version"`
	Commit         string          `json:"commit"`
	Environment    string          `json:"environment"`
	Coverage       string          `json:"coverage"`
	Files          []string        `json:"files"`
	Allocation     Allocation      `json:"allocation"`
	HistorySources []HistorySource `json:"history_sources"`
	Checksum       string          `json:"checksum"`
}

func NewPlan(options PlanOptions) (Plan, error) {
	if !validCommit(options.Commit) || !printable(options.Environment) {
		return Plan{}, fmt.Errorf("plan requires a full commit SHA and a printable runner environment")
	}
	if options.Shards < 1 || options.Shards > MaxShards {
		return Plan{}, fmt.Errorf("shard count must be between 1 and %d", MaxShards)
	}
	history := options.History
	if options.FallbackReason != "" {
		history = nil
	}
	allocation, err := Allocate(options.Files, options.Shards, options.Coverage, history)
	if err != nil {
		return Plan{}, err
	}
	if options.FallbackReason != "" && allocation.HasTests {
		allocation.FallbackReason = options.FallbackReason
	}
	files := append([]string{}, options.Files...)
	slices.Sort(files)
	sources := append([]HistorySource{}, options.HistorySources...)
	if allocation.FallbackReason != "" {
		sources = []HistorySource{}
	}
	plan := Plan{Version: 1, Commit: options.Commit, Environment: options.Environment, Coverage: options.Coverage, Files: files, Allocation: allocation, HistorySources: sources}
	plan.Checksum, err = plan.checksum()
	return plan, err
}

func ReadPlan(reader io.Reader) (Plan, error) {
	var plan Plan
	if err := decodeStrict(reader, &plan); err != nil {
		return Plan{}, err
	}
	return plan, plan.Verify(plan.Commit)
}

func (p Plan) Verify(commit string) error {
	if p.Version != 1 || !validCommit(p.Commit) || p.Commit != commit || !printable(p.Environment) {
		return fmt.Errorf("plan version, tested commit, or runner environment is invalid")
	}
	checksum, err := p.checksum()
	if err != nil || p.Checksum == "" || checksum != p.Checksum {
		return fmt.Errorf("assignment checksum does not match its contents")
	}
	if _, err := Allocate(p.Files, 1, p.Coverage, nil); err != nil {
		return err
	}
	if p.Allocation.HasTests != (len(p.Files) > 0) || len(p.Allocation.Shards) > MaxShards || (len(p.Files) > 0 && len(p.Allocation.Shards) == 0) || (len(p.Files) == 0 && len(p.Allocation.Shards) != 0) {
		return fmt.Errorf("assignment has an invalid worker count")
	}
	seen := map[string]bool{}
	required := make(map[string]bool, len(p.Files))
	for _, file := range p.Files {
		required[file] = true
	}
	for _, shard := range p.Allocation.Shards {
		if shard.PredictedSeconds < 0 || math.IsNaN(shard.PredictedSeconds) || math.IsInf(shard.PredictedSeconds, 0) {
			return fmt.Errorf("assignment contains invalid predicted durations")
		}
		for _, file := range shard.Files {
			if seen[file] || !required[file] {
				return fmt.Errorf("assignment duplicates or adds file %q", file)
			}
			seen[file] = true
		}
	}
	if len(seen) != len(p.Files) {
		return fmt.Errorf("assignment omits required files")
	}
	for file, estimate := range p.Allocation.Estimates {
		if !seen[file] || estimate.Samples < 0 || estimate.Samples > observationWindow || estimate.Seconds < 0 || math.IsNaN(estimate.Seconds) || math.IsInf(estimate.Seconds, 0) {
			return fmt.Errorf("assignment has an invalid estimate for %q", file)
		}
	}
	return nil
}

func (p Plan) Manifest(index int) ([]string, error) {
	if err := p.Verify(p.Commit); err != nil {
		return nil, err
	}
	if index < 0 || index >= len(p.Allocation.Shards) {
		return nil, fmt.Errorf("shard index %d is outside this assignment", index)
	}
	return slices.Clone(p.Allocation.Shards[index].Files), nil
}

func (p Plan) checksum() (string, error) {
	p.Checksum = ""
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
