package balance

import (
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

type Shard struct {
	Files            []string `json:"files"`
	PredictedSeconds float64  `json:"predicted_seconds"`
}

type Allocation struct {
	HasTests       bool                `json:"has_tests"`
	Shards         []Shard             `json:"shards"`
	Estimates      map[string]Estimate `json:"estimates"`
	FallbackReason string              `json:"fallback_reason,omitempty"`
}

// Allocate partitions the required files without discovering or selecting tests.
// Unknown history changes allocation strategy, never the required file list.
func Allocate(files []string, shardCount int, coverage string, history *History) (Allocation, error) {
	if shardCount < 1 || shardCount > MaxShards {
		return Allocation{}, fmt.Errorf("shard count must be between 1 and %d", MaxShards)
	}
	if coverage != "full" && coverage != "selected" {
		return Allocation{}, fmt.Errorf("coverage must be full or selected")
	}
	files = slices.Clone(files)
	slices.Sort(files)
	for i, file := range files {
		if err := validatePath(file); err != nil {
			return Allocation{}, err
		}
		if i > 0 && files[i-1] == file {
			return Allocation{}, fmt.Errorf("required file %q appears more than once", file)
		}
	}
	if len(files) == 0 {
		if coverage == "selected" {
			return Allocation{Shards: []Shard{}, Estimates: map[string]Estimate{}}, nil
		}
		return Allocation{}, fmt.Errorf("full-suite input contains no files; check your pattern or manifest")
	}

	estimates, known := fileEstimates(files, history)
	if len(known) == 0 {
		return roundRobin(files, shardCount, "No timing observations match the required files."), nil
	}
	slices.Sort(known)
	median := known[len(known)/2]
	if len(known)%2 == 0 {
		lower := known[len(known)/2-1]
		median = lower + (median-lower)/2
	}
	for _, file := range files {
		if _, ok := estimates[file]; !ok {
			estimates[file] = Estimate{Seconds: median}
		}
	}
	slices.SortStableFunc(files, func(a, b string) int {
		if estimates[a].Seconds > estimates[b].Seconds {
			return -1
		}
		if estimates[a].Seconds < estimates[b].Seconds {
			return 1
		}
		return strings.Compare(a, b)
	})

	allocation := Allocation{HasTests: true, Shards: makeShards(shardCount), Estimates: estimates}
	for _, file := range files {
		leastLoaded := 0
		for i := 1; i < shardCount; i++ {
			if allocation.Shards[i].PredictedSeconds < allocation.Shards[leastLoaded].PredictedSeconds {
				leastLoaded = i
			}
		}
		shard := &allocation.Shards[leastLoaded]
		shard.Files = append(shard.Files, file)
		shard.PredictedSeconds += estimates[file].Seconds
		if math.IsInf(shard.PredictedSeconds, 0) {
			slices.Sort(files)
			return roundRobin(files, shardCount, "Timing estimates overflow the predicted shard total."), nil
		}
	}
	return allocation, nil
}

func fileEstimates(files []string, history *History) (map[string]Estimate, []float64) {
	estimates := make(map[string]Estimate)
	var known []float64
	for _, file := range files {
		if estimate, ok := history.Estimate(file); ok {
			estimates[file] = estimate
			known = append(known, estimate.Seconds)
		}
	}
	return estimates, known
}

func makeShards(count int) []Shard {
	shards := make([]Shard, count)
	for i := range shards {
		shards[i].Files = []string{}
	}
	return shards
}

func roundRobin(files []string, shardCount int, reason string) Allocation {
	allocation := Allocation{HasTests: true, Shards: makeShards(shardCount), Estimates: map[string]Estimate{}, FallbackReason: reason}
	for i, file := range files {
		shard := &allocation.Shards[i%shardCount]
		shard.Files = append(shard.Files, file)
	}
	return allocation
}

func validatePath(file string) error {
	if !utf8.ValidString(file) || file == "" || file == "." || file == ".." || strings.HasPrefix(file, "/") || path.Clean(file) != file || strings.HasPrefix(file, "../") || strings.Contains(file, "\\") || (len(file) > 1 && file[1] == ':') {
		return fmt.Errorf("%q: file path must be relative to the repository root, without . or .. segments", file)
	}
	for _, r := range file {
		if r < 32 || r == 127 {
			return fmt.Errorf("%q: file path contains a control character", file)
		}
	}
	return nil
}
