package balance

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
)

type Receipt struct {
	Version      int    `json:"version"`
	PlanChecksum string `json:"plan_checksum"`
	Commit       string `json:"commit"`
	RunID        uint64 `json:"run_id"`
	Attempt      uint64 `json:"attempt"`
	Shard        int    `json:"shard"`
	Report       Report `json:"report"`
}

func NewReceipt(plan Plan, index int, runID, attempt uint64, report Report) (Receipt, error) {
	if err := plan.Verify(plan.Commit); err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{Version: 1, PlanChecksum: plan.Checksum, Commit: plan.Commit, RunID: runID, Attempt: attempt, Shard: index, Report: report}
	if err := receipt.validate(plan, runID, attempt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func ReadReceipt(reader io.Reader) (Receipt, error) {
	var input struct {
		Version      int        `json:"version"`
		PlanChecksum string     `json:"plan_checksum"`
		Commit       string     `json:"commit"`
		RunID        uint64     `json:"run_id"`
		Attempt      uint64     `json:"attempt"`
		Shard        *int       `json:"shard"`
		Report       wireReport `json:"report"`
	}
	if err := decodeStrict(reader, &input); err != nil {
		return Receipt{}, err
	}
	report, err := input.Report.report()
	if err != nil {
		return Receipt{}, err
	}
	if input.Version != 1 || !validCommit(input.Commit) || input.PlanChecksum == "" || input.RunID == 0 || input.Attempt == 0 || input.Shard == nil || *input.Shard < 0 {
		return Receipt{}, fmt.Errorf("receipt has invalid version or execution metadata")
	}
	return Receipt{input.Version, input.PlanChecksum, input.Commit, input.RunID, input.Attempt, *input.Shard, report}, nil
}

func (r Receipt) validate(plan Plan, runID, attempt uint64) error {
	if r.Version != 1 || r.PlanChecksum != plan.Checksum || r.Commit != plan.Commit || r.RunID == 0 || r.Attempt == 0 || r.RunID != runID || r.Attempt != attempt {
		return fmt.Errorf("shard %d receipt does not match this assignment, commit, run, and attempt", r.Shard)
	}
	if r.Shard < 0 || r.Shard >= len(plan.Allocation.Shards) || r.Report.Version != 1 || r.Report.Files == nil {
		return fmt.Errorf("receipt has invalid shard or report version")
	}
	for file, result := range r.Report.Files {
		if !slices.Contains(plan.Allocation.Shards[r.Shard].Files, file) {
			return fmt.Errorf("shard %d reported unassigned file %q", r.Shard, file)
		}
		if result.Status != "passed" && result.Status != "failed" {
			return fmt.Errorf("file %q has an invalid result status", file)
		}
		if err := (&History{}).Record(file, Sample{r.RunID, r.Attempt, result.Seconds}); err != nil {
			return err
		}
	}
	return nil
}

type FinishResult struct {
	Summary     string
	Observation *Observation
	Issues      []string
}

func Finish(plan Plan, source Source, receipts []Receipt, inputIssues []string) FinishResult {
	result := FinishResult{Issues: append([]string{}, inputIssues...)}
	if err := plan.Verify(source.Commit); err != nil {
		result.Issues = append(result.Issues, err.Error())
		result.Summary = "## Suite Balance\n\nInvalid assignment: " + markdown(err.Error()) + "\n"
		return result
	}
	if err := source.validate(); err != nil || source.Environment != plan.Environment || source.Coverage != plan.Coverage {
		result.Issues = append(result.Issues, "Source metadata does not match this plan.")
		result.Summary = "## Suite Balance\n\nSource metadata does not match this plan.\n"
		return result
	}
	counts := map[int]int{}
	for _, receipt := range receipts {
		counts[receipt.Shard]++
	}
	valid := map[int]Receipt{}
	for _, receipt := range receipts {
		if counts[receipt.Shard] != 1 {
			result.Issues = append(result.Issues, fmt.Sprintf("Shard %d reported more than once.", receipt.Shard))
			continue
		}
		if err := receipt.validate(plan, source.RunID, source.Attempt); err != nil {
			result.Issues = append(result.Issues, err.Error())
			continue
		}
		valid[receipt.Shard] = receipt
	}
	passing := map[string]FileResult{}
	for index, shard := range plan.Allocation.Shards {
		receipt, ok := valid[index]
		if !ok && len(shard.Files) > 0 {
			result.Issues = append(result.Issues, fmt.Sprintf("Shard %d has no valid report.", index))
			continue
		}
		for _, file := range shard.Files {
			fileResult, ok := receipt.Report.Files[file]
			if !ok {
				result.Issues = append(result.Issues, fmt.Sprintf("File %q has no complete result.", file))
				continue
			}
			if fileResult.Status != "passed" {
				result.Issues = append(result.Issues, fmt.Sprintf("File %q failed.", file))
				continue
			}
			passing[file] = fileResult
		}
	}
	if source.eligible() && len(passing) > 0 {
		result.Observation = &Observation{Version: 1, Source: source, Files: passing}
	}
	result.Summary = finishSummary(plan, valid, result)
	return result
}

func finishSummary(plan Plan, receipts map[int]Receipt, result FinishResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "## Suite Balance\n\n%d files · %d shards · environment: %s\n\n", len(plan.Files), len(plan.Allocation.Shards), markdown(plan.Environment))
	if !plan.Allocation.HasTests {
		out.WriteString("No tests selected. History unchanged.\n")
	}
	if reason := plan.Allocation.FallbackReason; reason != "" {
		fmt.Fprintf(&out, "**Fallback:** %s Using round-robin; required coverage is unchanged.\n\n", markdown(reason))
	} else if plan.Allocation.HasTests {
		minSamples, maxSamples, newFiles := observationWindow, 0, 0
		for _, estimate := range plan.Allocation.Estimates {
			if estimate.Samples == 0 {
				newFiles++
			} else {
				minSamples = min(minSamples, estimate.Samples)
				maxSamples = max(maxSamples, estimate.Samples)
			}
		}
		fmt.Fprintf(&out, "History: %d–%d observations per known file · %d new files · %d source run attempts.\n\n", minSamples, maxSamples, newFiles, len(plan.HistorySources))
	}
	out.WriteString("| Shard | Files | Predicted | Actual |\n| --- | ---: | ---: | ---: |\n")
	reported := 0
	totals := []float64{}
	largest, largestFile := 0.0, ""
	activeShards := 0
	for index, shard := range plan.Allocation.Shards {
		if len(shard.Files) > 0 {
			activeShards++
		}
		receipt, exists := receipts[index]
		total := 0.0
		for file, fileResult := range receipt.Report.Files {
			total += fileResult.Seconds
			if fileResult.Seconds > largest || (fileResult.Seconds == largest && file < largestFile) {
				largest, largestFile = fileResult.Seconds, file
			}
		}
		reported += len(receipt.Report.Files)
		predicted := "—"
		if plan.Allocation.FallbackReason == "" {
			predicted = fmt.Sprintf("%.2fs", shard.PredictedSeconds)
		}
		actual := "missing"
		if len(shard.Files) == 0 || (exists && len(receipt.Report.Files) == len(shard.Files)) {
			actual = fmt.Sprintf("%.2fs", total)
			if math.IsInf(total, 0) {
				actual = "total exceeds numeric range"
			} else if len(shard.Files) > 0 {
				totals = append(totals, total)
			}
		} else if exists {
			actual = fmt.Sprintf("%.2fs (incomplete)", total)
		}
		fmt.Fprintf(&out, "| %d | %d | %s | %s |\n", index, len(shard.Files), predicted, actual)
	}
	fmt.Fprintf(&out, "\nCoverage: **%d / %d** files reported.\n", reported, len(plan.Files))
	if len(totals) > 0 && len(totals) == activeShards && reported == len(plan.Files) {
		slices.Sort(totals)
		fmt.Fprintf(&out, "\nActual spread: **%.2fs**.\n", totals[len(totals)-1]-totals[0])
	} else if activeShards > 0 {
		out.WriteString("\nActual spread unavailable: one or more shard totals are incomplete or exceed the numeric range.\n")
	}
	if largestFile != "" {
		fmt.Fprintf(&out, "\nLargest file: %s (%.2fs). A whole-file assignment cannot finish faster than its slowest file.\n", markdown(largestFile), largest)
	}
	if result.Observation != nil {
		fmt.Fprintf(&out, "\nHistory: %d passing files eligible for publication.\n", len(result.Observation.Files))
	} else if plan.Allocation.HasTests {
		out.WriteString("\nHistory unchanged: no eligible passing observations.\n")
	}
	if len(result.Issues) > 0 {
		out.WriteString("\n### Issues\n\n")
		for _, issue := range result.Issues {
			fmt.Fprintf(&out, "- %s\n", markdown(issue))
		}
	}
	out.WriteString("\nExecution totals exclude queue, build, setup, and artifact-transfer time.\n")
	return out.String()
}

func markdown(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "\\|", "`", "\\`", "\n", " ", "\r", " ").Replace(text)
}
