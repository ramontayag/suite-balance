package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpAndVersion(t *testing.T) {
	for _, command := range [][]string{{"--help"}, {"plan", "--help"}, {"files", "--help"}, {"receipt", "--help"}, {"finish", "--help"}, {"explain", "--help"}} {
		var out, errOut bytes.Buffer
		if code := run(command, &out, &errOut); code != 0 || out.Len() == 0 {
			t.Errorf("run(%v) = %d, stdout %q, stderr %q", command, code, out.String(), errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) == "" {
		t.Fatalf("version = %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

func TestValidateCommittedUsesGitAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	file := filepath.Join(root, "test file.txt")
	if err := os.WriteFile(file, []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "--", "test file.txt")
	git("commit", "-qm", "fixture")
	cmd := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	commit, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(commit))
	if err := validateCommitted(root, sha, []string{"test file.txt"}); err != nil {
		t.Fatalf("committed path rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateCommitted(root, sha, []string{"untracked"}); err == nil {
		t.Fatal("untracked file accepted")
	}
	if err := os.Symlink(file, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := validateCommitted(root, sha, []string{"linked"}); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestUsageErrorsReturnTwo(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"plan", "--shards"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Errorf("run(%v) = %d, want 2; stderr %q", args, code, errOut.String())
		}
	}
}

func TestMissingShardIsUsageErrorAndRequiredNamesAreShown(t *testing.T) {
	for _, args := range [][]string{
		{"files", "--plan", "p", "--commit", strings.Repeat("a", 40), "--output", "o"},
		{"receipt", "--plan", "p", "--commit", strings.Repeat("a", 40), "--run-id", "1", "--attempt", "1", "--report", "r", "--output", "o"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--shard") {
			t.Errorf("run(%v) = %d, stderr %q", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"plan"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--files") {
		t.Fatalf("missing flag not named: %d %q", code, stderr.String())
	}
}

func TestHelpAnywhereReturnsSuccess(t *testing.T) {
	for _, args := range [][]string{{"plan", "--shards", "bad", "--help"}, {"files", "--plan", "missing", "--help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 || stdout.Len() == 0 {
			t.Errorf("run(%v) = %d, stdout %q", args, code, stdout.String())
		}
	}
}

func TestFinishOutputsCannotBeInsideReceiptDirectory(t *testing.T) {
	base := t.TempDir()
	reports := filepath.Join(base, "reports")
	if err := checkFinishOutputCollisions(filepath.Join(reports, "summary.md"), filepath.Join(base, "observation.json"), filepath.Join(base, "plan.json"), filepath.Join(base, "source.json"), reports, nil); err == nil {
		t.Fatal("summary must not be written into the receipt input directory, even when it cannot be enumerated")
	}
}

func TestHistoryInputHasAnAggregateSizeLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 17; i++ {
		file, err := os.Create(filepath.Join(dir, fmt.Sprintf("%02d.json", i)))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(8 << 20); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	if _, err := readObservations(dir); err == nil || !strings.Contains(err.Error(), "128 MiB") {
		t.Fatalf("oversized aggregate history must fail before parsing: %v", err)
	}
}

type cliFixture struct {
	t            *testing.T
	root, commit string
}

func newFixture(t *testing.T, files map[string]string) cliFixture {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v: %s", args, e, b)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	for name, contents := range files {
		path := filepath.Join(root, name)
		if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, []byte(contents), 0644); e != nil {
			t.Fatal(e)
		}
	}
	git("add", "--all")
	git("commit", "-qm", "fixture")
	c := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	b, e := c.Output()
	if e != nil {
		t.Fatal(e)
	}
	return cliFixture{t, root, strings.TrimSpace(string(b))}
}

func (f cliFixture) invoke(args ...string) (int, string, string) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}
func (f cliFixture) plan(files, output string, extra ...string) map[string]any {
	f.t.Helper()
	args := []string{"plan", "--files", files, "--shards", "1", "--coverage", "full", "--environment", "ubuntu-standard", "--commit", f.commit, "--root", f.root, "--output", output}
	args = append(args, extra...)
	code, out, errOut := f.invoke(args...)
	if code != 0 {
		f.t.Fatalf("plan exit %d: %s", code, errOut)
	}
	var result map[string]any
	if e := json.Unmarshal([]byte(out), &result); e != nil {
		f.t.Fatalf("plan stdout is not JSON: %v: %q", e, out)
	}
	return result
}
func (f cliFixture) finish(plan, dir, source, obs, summary string, run, attempt string) (int, string) {
	f.t.Helper()
	code, _, errOut := f.invoke("finish", "--plan", plan, "--commit", f.commit, "--run-id", run, "--attempt", attempt, "--reports-dir", dir, "--source", source, "--observation", obs, "--summary", summary)
	return code, errOut
}
func put(t *testing.T, path, content string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
}
func sourceJSON(repo, workflow, commit, env, coverage string, run, attempt int) string {
	b, _ := json.Marshal(map[string]any{"repository": repo, "workflow": workflow, "run_id": run, "attempt": attempt, "commit": commit, "environment": env, "coverage": coverage, "event": "push", "branch": "main", "default_branch": "main"})
	return string(b)
}

func TestRunFullCycleLearnsHistoryForNextPlan(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "package a", "test/b": "package b"})
	base := t.TempDir()
	manifest := filepath.Join(base, "files.txt")
	put(t, manifest, "test/a\ntest/b\n")
	result := f.plan(manifest, filepath.Join(base, "plan.json"))
	if result["has_tests"] != true {
		t.Fatalf("plan output omits has_tests=true: %#v", result)
	}
	for shard := 0; shard < 1; shard++ {
		list := filepath.Join(base, "assigned")
		code, _, errOut := f.invoke("files", "--plan", filepath.Join(base, "plan.json"), "--shard", fmt.Sprint(shard), "--commit", f.commit, "--output", list)
		if code != 0 {
			t.Fatal(errOut)
		}
		report := filepath.Join(base, "report.json")
		put(t, report, `{"version":1,"files":{"test/a":{"duration_seconds":2.5,"status":"passed"},"test/b":{"duration_seconds":4,"status":"passed"}}}`)
		code, _, errOut = f.invoke("receipt", "--plan", filepath.Join(base, "plan.json"), "--shard", fmt.Sprint(shard), "--commit", f.commit, "--run-id", "10", "--attempt", "1", "--report", report, "--output", filepath.Join(base, "receipts", "0.json"))
		if code != 0 {
			t.Fatal(errOut)
		}
	}
	source := filepath.Join(base, "source.json")
	put(t, source, sourceJSON("acme/repo", ".github/workflows/test.yml", f.commit, "ubuntu-standard", "full", 10, 1))
	obs := filepath.Join(base, "observation.json")
	if code, msg := f.finish(filepath.Join(base, "plan.json"), filepath.Join(base, "receipts"), source, obs, filepath.Join(base, "summary.md"), "10", "1"); code != 0 {
		t.Fatalf("finish = %d: %s", code, msg)
	}
	var observation map[string]any
	data, e := os.ReadFile(obs)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &observation); e != nil {
		t.Fatal(e)
	}
	// History contains only immutable observations, not plans or receipts.
	historyDir := filepath.Join(base, "history")
	if e = os.Mkdir(historyDir, 0700); e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(historyDir, "observation.json"), string(data))
	second := f.plan(manifest, filepath.Join(base, "next-history.json"), "--history-dir", historyDir)
	if second["fallback_reason"] != "" {
		t.Fatalf("compatible history fell back: %#v", second)
	}
	nextBytes, e := os.ReadFile(filepath.Join(base, "next-history.json"))
	if e != nil {
		t.Fatal(e)
	}
	var next struct {
		HistorySources []json.RawMessage `json:"history_sources"`
		Allocation     struct {
			Estimates map[string]struct {
				Samples int `json:"samples"`
			} `json:"estimates"`
		} `json:"allocation"`
	}
	if e = json.Unmarshal(nextBytes, &next); e != nil {
		t.Fatal(e)
	}
	if len(next.HistorySources) != 1 || next.Allocation.Estimates["test/a"].Samples != 1 {
		t.Fatalf("history not used by next plan: %#v", next)
	}
}

func TestPlanStdoutUsesGitHubMatrixShape(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest, output := filepath.Join(d, "files"), filepath.Join(d, "plan.json")
	put(t, manifest, "test/a\n")
	_, stdout, _ := f.invoke("plan", "--files", manifest, "--shards", "3", "--coverage", "full", "--environment", "env", "--commit", f.commit, "--root", f.root, "--output", output)
	var got struct {
		Matrix   map[string][]int `json:"matrix"`
		HasTests bool             `json:"has_tests"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if !got.HasTests || len(got.Matrix) != 1 || len(got.Matrix["shard"]) != 1 || got.Matrix["shard"][0] != 0 {
		t.Fatalf("unexpected matrix output: %s", stdout)
	}
	if !strings.Contains(stdout, `"matrix":{"shard":[0]}`) {
		t.Fatalf("stdout does not have the required matrix shape: %s", stdout)
	}
}

func TestHistoryOutputCollisionRejectedEvenWithFallback(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "test/a\n")
	history := filepath.Join(d, "history")
	if err := os.Mkdir(history, 0700); err != nil {
		t.Fatal(err)
	}
	observation := filepath.Join(history, "observation.json")
	put(t, observation, `{"version":1,"source":{"repository":"r","workflow":"w","run_id":1,"attempt":1,"commit":"`+f.commit+`","environment":"env","coverage":"full","event":"push","branch":"main","default_branch":"main"},"files":{"test/a":{"duration_seconds":1,"status":"passed"}}}`)
	for _, extra := range [][]string{{"--history-dir", history}, {"--history-dir", history, "--fallback-reason", "forced"}} {
		args := []string{"plan", "--files", manifest, "--shards", "1", "--coverage", "full", "--environment", "env", "--commit", f.commit, "--root", f.root, "--output", observation}
		args = append(args, extra...)
		if code, _, _ := f.invoke(args...); code != 1 {
			t.Fatalf("history overwrite accepted (extra %v): exit %d", extra, code)
		}
		data, err := os.ReadFile(observation)
		if err != nil || !strings.Contains(string(data), `"source"`) {
			t.Fatalf("history changed: %v", err)
		}
	}
}

func TestCRLFManifestAndEmptyLines(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a", "test/b": "b"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	output := filepath.Join(d, "plan.json")
	put(t, manifest, "test/a\r\ntest/b\r\n")
	result := f.plan(manifest, output)
	if result["has_tests"] != true {
		t.Fatalf("CRLF manifest rejected or truncated: %#v", result)
	}
	put(t, manifest, "test/a\n\ntest/b\n")
	code, _, _ := f.invoke("plan", "--files", manifest, "--shards", "1", "--coverage", "full", "--environment", "env", "--commit", f.commit, "--root", f.root, "--output", output)
	if code != 1 {
		t.Fatalf("manifest with empty internal path returned %d", code)
	}
}

func TestOutputAliasCannotOverwriteInput(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	real := filepath.Join(d, "real")
	alias := filepath.Join(d, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(real, "manifest")
	put(t, manifest, "test/a\n")
	outputAlias := filepath.Join(alias, "manifest")
	args := []string{"plan", "--files", manifest, "--shards", "1", "--coverage", "full", "--environment", "env", "--commit", f.commit, "--root", f.root, "--output", outputAlias}
	if code, _, _ := f.invoke(args...); code != 1 {
		t.Fatalf("symlink-alias output overwrote manifest, exit %d", code)
	}
	data, err := os.ReadFile(manifest)
	if err != nil || string(data) != "test/a\n" {
		t.Fatalf("manifest changed: %q, %v", data, err)
	}
}

func TestPartialFailureStillPublishesPassingFile(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a", "test/b": "b"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "test/a\ntest/b\n")
	f.plan(manifest, filepath.Join(d, "plan.json"))
	put(t, filepath.Join(d, "report.json"), `{"version":1,"files":{"test/a":{"duration_seconds":2,"status":"passed"},"test/b":{"duration_seconds":3,"status":"failed"}}}`)
	code, _, errOut := f.invoke("receipt", "--plan", filepath.Join(d, "plan.json"), "--shard", "0", "--commit", f.commit, "--run-id", "22", "--attempt", "1", "--report", filepath.Join(d, "report.json"), "--output", filepath.Join(d, "receipts", "0.json"))
	if code != 0 {
		t.Fatal(errOut)
	}
	src := filepath.Join(d, "source")
	put(t, src, sourceJSON("acme/repo", "test.yml", f.commit, "ubuntu-standard", "full", 22, 1))
	obs := filepath.Join(d, "obs")
	if code, msg := f.finish(filepath.Join(d, "plan.json"), filepath.Join(d, "receipts"), src, obs, filepath.Join(d, "summary"), "22", "1"); code != 1 {
		t.Fatalf("finish exit=%d, want 1: %s", code, msg)
	}
	data, e := os.ReadFile(obs)
	if e != nil {
		t.Fatalf("passing result not recorded: %v", e)
	}
	var got struct {
		Files map[string]any `json:"files"`
	}
	if e = json.Unmarshal(data, &got); e != nil || len(got.Files) != 1 || got.Files["test/a"] == nil {
		t.Fatalf("observation files = %#v, err %v", got.Files, e)
	}
}

func TestSelectedEmptyPlanFinishesWithoutObservation(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest := filepath.Join(d, "empty")
	put(t, manifest, "")
	args := []string{"plan", "--files", manifest, "--shards", "3", "--coverage", "selected", "--environment", "ubuntu-standard", "--commit", f.commit, "--root", f.root, "--output", filepath.Join(d, "plan")}
	code, out, errOut := f.invoke(args...)
	if code != 0 {
		t.Fatal(errOut)
	}
	var planOut map[string]any
	_ = json.Unmarshal([]byte(out), &planOut)
	matrix := planOut["matrix"].(map[string]any)
	if planOut["has_tests"] != false || len(matrix) != 1 || len(matrix["shard"].([]any)) != 0 {
		t.Fatalf("empty plan output: %#v", planOut)
	}
	if !strings.Contains(out, `"matrix":{"shard":[]}`) {
		t.Fatalf("empty matrix has wrong shape: %s", out)
	}
	src := filepath.Join(d, "source")
	put(t, src, sourceJSON("acme/repo", "test.yml", f.commit, "ubuntu-standard", "selected", 1, 1))
	if code, msg := f.finish(filepath.Join(d, "plan"), filepath.Join(d, "none"), src, filepath.Join(d, "obs"), filepath.Join(d, "summary"), "1", "1"); code != 0 {
		t.Fatalf("finish empty plan=%d: %s", code, msg)
	}
	if _, e := os.Stat(filepath.Join(d, "obs")); !os.IsNotExist(e) {
		t.Fatalf("empty plan wrote observation: %v", e)
	}
}

func TestHistoryFailuresUseVisibleFallback(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "test/a\n")
	for name, contents := range map[string]string{"missing": "", "corrupt": "not json", "incompatible": ""} {
		dir := filepath.Join(d, name)
		if name != "missing" {
			if e := os.Mkdir(dir, 0700); e != nil {
				t.Fatal(e)
			}
			if name == "corrupt" {
				put(t, filepath.Join(dir, "bad.json"), contents)
			} else {
				put(t, filepath.Join(dir, "wrong.json"), `{"version":1,"source":{"repository":"acme/repo","workflow":"test.yml","run_id":1,"attempt":1,"commit":"`+f.commit+`","environment":"other","coverage":"full","event":"push","branch":"main","default_branch":"main"},"files":{"test/a":{"duration_seconds":1,"status":"passed"}}}`)
			}
		}
		out := filepath.Join(d, name+"-plan")
		res := f.plan(manifest, out, "--history-dir", dir)
		reason, ok := res["fallback_reason"].(string)
		if !ok || reason == "" {
			t.Fatalf("%s history fallback not surfaced: %#v", name, res)
		}
	}
}

func TestRequiredFilesMustBeCommittedRegularFilesAtCommit(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a", "test/link": "target"})
	if e := os.Remove(filepath.Join(f.root, "test/link")); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("a", filepath.Join(f.root, "test/link")); e != nil {
		t.Fatal(e)
	}
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "uncommitted\n")
	args := []string{"plan", "--files", manifest, "--shards", "1", "--coverage", "full", "--environment", "env", "--commit", f.commit, "--root", f.root, "--output", filepath.Join(d, "plan")}
	if code, _, _ := f.invoke(args...); code != 1 {
		t.Fatalf("uncommitted file exit %d", code)
	}
	put(t, manifest, "test/link\n")
	if code, _, _ := f.invoke(args...); code != 1 {
		t.Fatalf("committed symlink exit %d", code)
	}
	put(t, manifest, "test/a\n")
	args[10] = strings.Repeat("0", 40)
	if code, _, _ := f.invoke(args...); code != 1 {
		t.Fatalf("wrong commit exit %d", code)
	}
}

func TestFinishRejectsWrongAttemptAndDuplicateShard(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "test/a\n")
	f.plan(manifest, filepath.Join(d, "plan"))
	put(t, filepath.Join(d, "report"), `{"version":1,"files":{"test/a":{"duration_seconds":1,"status":"passed"}}}`)
	for _, attempt := range []string{"2", "1"} {
		receipt := filepath.Join(d, "receipts", attempt+".json")
		_, _, errOut := f.invoke("receipt", "--plan", filepath.Join(d, "plan"), "--shard", "0", "--commit", f.commit, "--run-id", "3", "--attempt", attempt, "--report", filepath.Join(d, "report"), "--output", receipt)
		if errOut != "" {
			t.Fatal(errOut)
		}
	}
	src := filepath.Join(d, "source")
	put(t, src, sourceJSON("acme/repo", "test.yml", f.commit, "ubuntu-standard", "full", 3, 1))
	code, msg := f.finish(filepath.Join(d, "plan"), filepath.Join(d, "receipts"), src, filepath.Join(d, "obs"), filepath.Join(d, "summary"), "3", "1")
	if code != 1 || !strings.Contains(msg, "more than once") {
		t.Fatalf("duplicate shards: %d %s", code, msg)
	}
	if _, e := os.Stat(filepath.Join(d, "obs")); !os.IsNotExist(e) {
		t.Fatalf("invalid duplicate receipts contaminated history: %v", e)
	}
	if e := os.Remove(filepath.Join(d, "receipts", "1.json")); e != nil {
		t.Fatal(e)
	}
	code, msg = f.finish(filepath.Join(d, "plan"), filepath.Join(d, "receipts"), src, filepath.Join(d, "obs"), filepath.Join(d, "summary"), "3", "1")
	if code != 1 || !strings.Contains(msg, "attempt") {
		t.Fatalf("wrong attempt: %d %s", code, msg)
	}
	if _, e := os.Stat(filepath.Join(d, "obs")); !os.IsNotExist(e) {
		t.Fatalf("mismatched receipt contaminated history: %v", e)
	}
}

func TestCommandsDoNotOverwriteInputFiles(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "test/a\n")
	plan := filepath.Join(d, "plan")
	f.plan(manifest, plan)
	if code, _, _ := f.invoke("files", "--plan", plan, "--shard", "0", "--commit", f.commit, "--output", plan); code != 1 {
		t.Fatalf("files overwrote plan, exit %d", code)
	}
	put(t, filepath.Join(d, "report"), `{"version":1,"files":{"test/a":{"duration_seconds":1,"status":"passed"}}}`)
	if code, _, _ := f.invoke("receipt", "--plan", plan, "--shard", "0", "--commit", f.commit, "--run-id", "4", "--attempt", "1", "--report", filepath.Join(d, "report"), "--output", filepath.Join(d, "report")); code != 1 {
		t.Fatalf("receipt overwrote report, exit %d", code)
	}
}

func TestFinishWritesSummaryForInvalidSourceAndReceiptDirectory(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "test/a\n")
	plan := filepath.Join(d, "plan.json")
	f.plan(manifest, plan)
	for _, tc := range []struct {
		name, source string
		reports      string
	}{
		{name: "malformed", source: "not json", reports: filepath.Join(d, "reports-malformed")},
		{name: "mismatched", source: sourceJSON("acme/repo", "test.yml", f.commit, "env", "full", 12, 1), reports: filepath.Join(d, "reports-mismatch")},
		{name: "receipt directory", source: sourceJSON("acme/repo", "test.yml", f.commit, "env", "full", 11, 1), reports: filepath.Join(d, "not-a-directory")},
	} {
		source := filepath.Join(d, tc.name+".source")
		put(t, source, tc.source)
		if tc.name == "receipt directory" {
			put(t, tc.reports, "file")
		}
		summary, observation := filepath.Join(d, tc.name+".md"), filepath.Join(d, tc.name+".observation")
		code, _, errText := f.invoke("finish", "--plan", plan, "--commit", f.commit, "--run-id", "11", "--attempt", "1", "--reports-dir", tc.reports, "--source", source, "--observation", observation, "--summary", summary)
		if code != 1 {
			t.Errorf("%s finish exit %d, stderr %s", tc.name, code, errText)
		}
		if data, err := os.ReadFile(summary); err != nil || !strings.Contains(string(data), "Finish validation failed") {
			t.Errorf("%s summary missing: %q, %v", tc.name, data, err)
		}
		if _, err := os.Stat(observation); !os.IsNotExist(err) {
			t.Errorf("%s wrote observation: %v", tc.name, err)
		}
	}
}

func TestInvalidSourceOutputCollisionsPreserveReceiptInputs(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "test/a\n")
	plan := filepath.Join(d, "plan")
	f.plan(manifest, plan)
	reports := filepath.Join(d, "reports")
	if err := os.Mkdir(reports, 0700); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(reports, "receipt.json")
	put(t, receipt, "preserve me")
	source := filepath.Join(d, "source")
	put(t, source, "not json")
	for _, output := range []string{receipt, filepath.Join(reports, "alias.json")} {
		observation := output
		if output != receipt {
			if err := os.Symlink(receipt, output); err != nil {
				t.Fatal(err)
			}
		}
		code, _, _ := f.invoke("finish", "--plan", plan, "--commit", f.commit, "--run-id", "1", "--attempt", "1", "--reports-dir", reports, "--source", source, "--observation", observation, "--summary", filepath.Join(d, "summary"))
		if code != 1 {
			t.Fatalf("collision accepted for %s", output)
		}
		if data, err := os.ReadFile(receipt); err != nil || string(data) != "preserve me" {
			t.Fatalf("receipt input changed: %q %v", data, err)
		}
	}
}

func TestFinishFailsWhenStaleObservationCannotBeRemoved(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a"})
	d := t.TempDir()
	manifest := filepath.Join(d, "files")
	put(t, manifest, "")
	plan := filepath.Join(d, "plan")
	args := []string{"plan", "--files", manifest, "--shards", "1", "--coverage", "selected", "--environment", "env", "--commit", f.commit, "--root", f.root, "--output", plan}
	if code, _, errText := f.invoke(args...); code != 0 {
		t.Fatal(errText)
	}
	source := filepath.Join(d, "source")
	put(t, source, sourceJSON("acme/repo", "test.yml", f.commit, "env", "selected", 1, 1))
	reports := filepath.Join(d, "reports")
	if err := os.Mkdir(reports, 0700); err != nil {
		t.Fatal(err)
	}
	observation := filepath.Join(d, "observation")
	if err := os.Mkdir(observation, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(observation, "keep"), "old")
	code, _, _ := f.invoke("finish", "--plan", plan, "--commit", f.commit, "--run-id", "1", "--attempt", "1", "--reports-dir", reports, "--source", source, "--observation", observation, "--summary", filepath.Join(d, "summary"))
	if code != 1 {
		t.Fatalf("finish succeeded despite observation cleanup failure")
	}
	if _, err := os.Stat(filepath.Join(observation, "keep")); err != nil {
		t.Fatalf("cleanup destroyed existing data: %v", err)
	}
}

func TestWriteJSONRejectsOutputOverEightMiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.json")
	err := writeJSON(path, strings.Repeat("x", 8<<20))
	if err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("writeJSON error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversize output was written: %v", err)
	}
}

func TestExplainIncludesPerFileAssignmentReason(t *testing.T) {
	f := newFixture(t, map[string]string{"test/a": "a", "test/new": "new"})
	d := t.TempDir()
	manifest, plan := filepath.Join(d, "files"), filepath.Join(d, "plan")
	put(t, manifest, "test/a\ntest/new\n")
	f.plan(manifest, plan)
	code, _, errText := f.invoke("explain", "--plan", plan, "--commit", f.commit)
	if code != 0 {
		t.Fatal(errText)
	}
	// No history was supplied, so both files use the round-robin fallback.
	_, text, _ := f.invoke("explain", "--plan", plan, "--commit", f.commit)
	if !strings.Contains(text, "Shard 0: test/a") || !strings.Contains(text, "round-robin, no estimate") {
		t.Fatalf("explain omitted per-file assignment detail: %s", text)
	}
}
