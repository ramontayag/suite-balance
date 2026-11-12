package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	balance "github.com/ramontayag/suite-balance"
)

var version = "dev"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	if args[0] == "--help" || args[0] == "-h" {
		usage(stdout)
		return 0
	}
	for _, arg := range args[1:] {
		if arg == "--help" || arg == "-h" {
			fmt.Fprintf(stdout, "Usage: suite-balance %s [flags]\n", args[0])
			switch args[0] {
			case "plan":
				fmt.Fprintln(stdout, "Required: --files, --shards, --coverage, --environment, --commit, --output. Optional: --history-dir, --root, --fallback-reason.")
			case "files":
				fmt.Fprintln(stdout, "Required: --plan, --shard, --commit, --output.")
			case "receipt":
				fmt.Fprintln(stdout, "Required: --plan, --shard, --commit, --run-id, --attempt, --report, --output.")
			case "finish":
				fmt.Fprintln(stdout, "Required: --plan, --commit, --run-id, --attempt, --reports-dir, --source, --observation, --summary.")
			case "explain":
				fmt.Fprintln(stdout, "Required: --plan, --commit.")
			case "version":
				fmt.Fprintln(stdout, "Print the suite-balance version.")
			default:
				usage(stdout)
				return 2
			}
			return 0
		}
	}
	if args[0] == "--version" || args[0] == "version" {
		if len(args) > 1 && args[1] != "--help" {
			fmt.Fprintln(stderr, "version takes no arguments")
			return 2
		}
		if len(args) > 1 {
			fmt.Fprintln(stdout, "Print the suite-balance version.")
			return 0
		}
		fmt.Fprintln(stdout, version)
		return 0
	}
	var err error
	switch args[0] {
	case "plan":
		err = planCommand(args[1:], stdout, stderr)
	case "files":
		err = filesCommand(args[1:], stdout, stderr)
	case "receipt":
		err = receiptCommand(args[1:], stdout, stderr)
	case "finish":
		err = finishCommand(args[1:], stdout, stderr)
	case "explain":
		err = explainCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
	if err == nil {
		return 0
	}
	var usageErr usageError
	if errors.As(err, &usageErr) {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stderr, err)
	return 1
}

type usageError struct{ message string }

var errHistoryOutputCollision = errors.New("plan output would overwrite a history observation")

func (e usageError) Error() string { return e.message }
func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: suite-balance <plan|files|receipt|finish|explain|version> [flags]")
}
func requireFlags(f *flag.FlagSet, names ...string) error {
	for _, name := range names {
		item := f.Lookup(name)
		if item == nil || strings.TrimSpace(item.Value.String()) == "" {
			return usageError{fmt.Sprintf("required flag --%s is missing", name)}
		}
	}
	return nil
}

func planCommand(args []string, out, stderr io.Writer) error {
	f := flag.NewFlagSet("plan", flag.ContinueOnError)
	f.SetOutput(stderr)
	files := f.String("files", "", "newline manifest")
	shards := f.Int("shards", 0, "number of shards")
	coverage := f.String("coverage", "", "full or selected")
	env := f.String("environment", "", "execution environment")
	commit := f.String("commit", "", "tested commit")
	hist := f.String("history-dir", "", "directory of observations")
	output := f.String("output", "", "plan output path")
	root := f.String("root", ".", "repository root")
	fallback := f.String("fallback-reason", "", "force round-robin fallback")
	if err := f.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if f.NArg() != 0 {
		return usageError{"unexpected positional arguments"}
	}
	if err := requireFlags(f, "files", "coverage", "environment", "commit", "output"); err != nil {
		return err
	}
	if *shards < 1 || *shards > balance.MaxShards {
		return usageError{fmt.Sprintf("--shards must be between 1 and %d", balance.MaxShards)}
	}
	manifest, err := readManifest(*files)
	if err != nil {
		return err
	}
	if err = validateCommitted(*root, *commit, manifest); err != nil {
		return err
	}
	var history *balance.History
	var sources []balance.HistorySource
	reason := *fallback
	if *hist != "" {
		if e := rejectOutputInHistory(*output, *hist); e != nil {
			return e
		}
	}
	if reason == "" && *hist != "" {
		observations, e := readObservations(*hist)
		if e == nil {
			history, sources, e = balance.BuildHistory(observations, *env)
		}
		if e != nil {
			reason = "history unavailable: " + e.Error()
			history = nil
			sources = nil
		}
	}
	p, err := balance.NewPlan(balance.PlanOptions{Files: manifest, Shards: *shards, Coverage: *coverage, Environment: *env, Commit: *commit, History: history, HistorySources: sources, FallbackReason: reason})
	if err != nil {
		return err
	}
	if samePath(*files, *output) {
		return fmt.Errorf("output must not overwrite the input manifest")
	}
	if err = writeJSON(*output, p); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Matrix   map[string][]int `json:"matrix"`
		HasTests bool             `json:"has_tests"`
		Checksum string           `json:"checksum"`
		Fallback string           `json:"fallback_reason"`
	}{matrix(p), len(p.Files) > 0, p.Checksum, p.Allocation.FallbackReason})
}

func rejectOutputInHistory(output, historyDir string) error {
	err := filepath.WalkDir(historyDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if samePath(output, path) {
			return fmt.Errorf("%w: %q", errHistoryOutputCollision, path)
		}
		return nil
	})
	if errors.Is(err, errHistoryOutputCollision) {
		return err
	}
	return nil
}

func filesCommand(args []string, out, stderr io.Writer) error {
	f := flag.NewFlagSet("files", flag.ContinueOnError)
	f.SetOutput(stderr)
	pf := f.String("plan", "", "plan JSON")
	shard := f.Int("shard", -1, "shard index")
	commit := f.String("commit", "", "tested commit")
	output := f.String("output", "", "manifest output")
	if e := f.Parse(args); e != nil {
		return usageError{e.Error()}
	}
	if f.NArg() != 0 {
		return usageError{"unexpected positional arguments"}
	}
	if e := requireFlags(f, "plan", "commit", "output"); e != nil {
		return e
	}
	if *shard < 0 {
		return usageError{"required flag --shard must be a nonnegative index"}
	}
	p, e := loadPlan(*pf, *commit)
	if e != nil {
		return e
	}
	if samePath(*output, *pf) {
		return fmt.Errorf("output must not overwrite the plan")
	}
	files, e := p.Manifest(*shard)
	if e != nil {
		return e
	}
	return writeBytes(*output, []byte(strings.Join(files, "\n")+trailingNewline(files)))
}
func receiptCommand(args []string, out, stderr io.Writer) error {
	f := flag.NewFlagSet("receipt", flag.ContinueOnError)
	f.SetOutput(stderr)
	pf := f.String("plan", "", "plan JSON")
	shard := f.Int("shard", -1, "shard index")
	commit := f.String("commit", "", "tested commit")
	run := f.Uint64("run-id", 0, "workflow run ID")
	attempt := f.Uint64("attempt", 0, "run attempt")
	report := f.String("report", "", "timing report JSON")
	output := f.String("output", "", "receipt output")
	if e := f.Parse(args); e != nil {
		return usageError{e.Error()}
	}
	if f.NArg() != 0 {
		return usageError{"unexpected positional arguments"}
	}
	if e := requireFlags(f, "plan", "commit", "report", "output"); e != nil {
		return e
	}
	if *shard < 0 {
		return usageError{"required flag --shard must be a nonnegative index"}
	}
	if *run == 0 || *attempt == 0 {
		return usageError{"--run-id and --attempt must be positive"}
	}
	p, e := loadPlan(*pf, *commit)
	if e != nil {
		return e
	}
	rfile, e := os.Open(*report)
	if e != nil {
		return e
	}
	r, e := balance.ReadReport(rfile)
	closeErr := rfile.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	rc, e := balance.NewReceipt(p, *shard, *run, *attempt, r)
	if e != nil {
		return e
	}
	if samePath(*output, *report) || samePath(*output, *pf) {
		return fmt.Errorf("output must not overwrite an input file")
	}
	return writeJSON(*output, rc)
}
func finishCommand(args []string, out, stderr io.Writer) error {
	f := flag.NewFlagSet("finish", flag.ContinueOnError)
	f.SetOutput(stderr)
	pf := f.String("plan", "", "plan JSON")
	commit := f.String("commit", "", "tested commit")
	run := f.Uint64("run-id", 0, "workflow run ID")
	attempt := f.Uint64("attempt", 0, "run attempt")
	dir := f.String("reports-dir", "", "receipt directory")
	sourcePath := f.String("source", "", "source metadata JSON")
	observation := f.String("observation", "", "observation output")
	summary := f.String("summary", "", "markdown summary output")
	if e := f.Parse(args); e != nil {
		return usageError{e.Error()}
	}
	if f.NArg() != 0 {
		return usageError{"unexpected positional arguments"}
	}
	if e := requireFlags(f, "plan", "commit", "reports-dir", "source", "observation", "summary"); e != nil {
		return e
	}
	if *run == 0 || *attempt == 0 {
		return usageError{"--run-id and --attempt must be positive"}
	}
	p, e := loadPlan(*pf, *commit)
	if e != nil {
		return e
	}
	entries, dirErr := os.ReadDir(*dir)
	if os.IsNotExist(dirErr) {
		entries, dirErr = nil, nil
	}
	if err := checkFinishOutputCollisions(*summary, *observation, *pf, *sourcePath, *dir, entries); err != nil {
		return err
	}
	if dirErr != nil {
		return finishValidationFailure(*summary, *observation, fmt.Sprintf("could not read receipt directory: %v", dirErr))
	}
	sf, e := os.Open(*sourcePath)
	if e != nil {
		return finishValidationFailure(*summary, *observation, fmt.Sprintf("could not read source metadata: %v", e))
	}
	source, e := balance.ReadSource(sf)
	closeErr := sf.Close()
	if e != nil {
		return finishValidationFailure(*summary, *observation, fmt.Sprintf("invalid source metadata: %v", e))
	}
	if closeErr != nil {
		return finishValidationFailure(*summary, *observation, fmt.Sprintf("could not close source metadata: %v", closeErr))
	}
	if source.Commit != *commit || source.RunID != *run || source.Attempt != *attempt {
		return finishValidationFailure(*summary, *observation, "source provenance does not match commit, run ID, and attempt")
	}
	receipts := make([]balance.Receipt, 0)
	issues := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(*dir, entry.Name())
		file, e := os.Open(path)
		if e != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", entry.Name(), e))
			continue
		}
		r, e := balance.ReadReceipt(file)
		file.Close()
		if e != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", entry.Name(), e))
			continue
		}
		receipts = append(receipts, r)
	}
	result := balance.Finish(p, source, receipts, issues)
	if err := removeIfExists(*observation); err != nil {
		_ = writeBytes(*summary, []byte("## Suite Balance\n\nFinish failed: could not clear previous observation: "+err.Error()+"\n"))
		return fmt.Errorf("clear previous observation: %w", err)
	}
	if e = writeBytes(*summary, []byte(result.Summary)); e != nil {
		return e
	}
	if result.Observation != nil {
		if e = writeJSON(*observation, *result.Observation); e != nil {
			_ = removeIfExists(*observation)
			return e
		}
	}
	if len(result.Issues) > 0 {
		return fmt.Errorf("%s", strings.Join(result.Issues, "; "))
	}
	return nil
}

func checkFinishOutputCollisions(summary, observation, plan, source, reportsDir string, entries []os.DirEntry) error {
	for _, output := range []string{summary, observation} {
		relative, err := filepath.Rel(canonicalPath(reportsDir), canonicalPath(output))
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("outputs must be outside the receipt input directory")
		}
	}
	if samePath(summary, plan) || samePath(summary, source) || samePath(observation, plan) || samePath(observation, source) || samePath(observation, summary) || samePath(summary, reportsDir) || samePath(observation, reportsDir) {
		return fmt.Errorf("outputs must not overwrite input files or each other")
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		input := filepath.Join(reportsDir, entry.Name())
		if samePath(summary, input) || samePath(observation, input) {
			return fmt.Errorf("outputs must not overwrite receipt inputs")
		}
	}
	return nil
}

func finishValidationFailure(summary, observation, message string) error {
	cleanupErr := removeIfExists(observation)
	summaryErr := writeBytes(summary, []byte("## Suite Balance\n\nFinish validation failed: "+message+"\n"))
	if cleanupErr != nil || summaryErr != nil {
		return errors.Join(fmt.Errorf("%s", message), cleanupErr, summaryErr)
	}
	return fmt.Errorf("%s", message)
}

func explainCommand(args []string, out, stderr io.Writer) error {
	f := flag.NewFlagSet("explain", flag.ContinueOnError)
	f.SetOutput(stderr)
	pf := f.String("plan", "", "plan JSON")
	commit := f.String("commit", "", "tested commit")
	if e := f.Parse(args); e != nil {
		return usageError{e.Error()}
	}
	if f.NArg() != 0 {
		return usageError{"unexpected positional arguments"}
	}
	if e := requireFlags(f, "plan", "commit"); e != nil {
		return e
	}
	p, e := loadPlan(*pf, *commit)
	if e != nil {
		return e
	}
	fmt.Fprintf(out, "Plan for %s (%s coverage): %d files across %d shards.\n", p.Commit, p.Coverage, len(p.Files), len(p.Allocation.Shards))
	if p.Allocation.FallbackReason != "" {
		fmt.Fprintf(out, "Fallback: %s\n", p.Allocation.FallbackReason)
	}
	for index, shard := range p.Allocation.Shards {
		for _, file := range shard.Files {
			estimate, known := p.Allocation.Estimates[file]
			if !known {
				fmt.Fprintf(out, "Shard %d: %s — no estimate (round-robin, no estimate).\n", index, file)
			} else if estimate.Samples == 0 {
				fmt.Fprintf(out, "Shard %d: %s — estimated %.2fs (median for new file).\n", index, file, estimate.Seconds)
			} else {
				fmt.Fprintf(out, "Shard %d: %s — estimated %.2fs (%d observations).\n", index, file, estimate.Seconds, estimate.Samples)
			}
		}
	}
	return nil
}

func loadPlan(path, commit string) (balance.Plan, error) {
	f, e := os.Open(path)
	if e != nil {
		return balance.Plan{}, e
	}
	defer f.Close()
	p, e := balance.ReadPlan(f)
	if e != nil {
		return p, e
	}
	if e = p.Verify(commit); e != nil {
		return p, e
	}
	return p, nil
}
func readManifest(path string) ([]string, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("manifest exceeds the 8 MiB input limit")
	}
	if len(data) == 0 {
		return []string{}, nil
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.ContainsRune(text, '\r') {
		return nil, fmt.Errorf("manifest contains an invalid carriage return")
	}
	if text == "" {
		return []string{}, nil
	}
	text = strings.TrimSuffix(text, "\n")
	files := strings.Split(text, "\n")
	for _, file := range files {
		if file == "" {
			return nil, fmt.Errorf("manifest contains an empty path")
		}
	}
	return files, nil
}
func validateCommitted(root, commit string, files []string) error {
	cmd := exec.Command("git", "-C", root, "ls-tree", "-r", "-z", commit)
	listing, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git tree lookup at %s: %s: %w", commit, strings.TrimSpace(string(listing)), err)
	}
	committed := make(map[string]bool)
	for _, line := range bytes.Split(listing, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte("\t"), 2)
		if len(parts) != 2 {
			continue
		}
		metadata := strings.Fields(string(parts[0]))
		if len(metadata) == 3 && metadata[1] == "blob" && (metadata[0] == "100644" || metadata[0] == "100755") {
			committed[string(parts[1])] = true
		}
	}
	for _, name := range files {
		if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") {
			return fmt.Errorf("invalid manifest path %q", name)
		}
		clean := filepath.ToSlash(filepath.Clean(name))
		if clean != name || clean == "." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("invalid manifest path %q", name)
		}
		if !committed[name] {
			return fmt.Errorf("%q is not committed at %s", name, commit)
		}
		full := filepath.Join(root, filepath.FromSlash(name))
		rel, e := filepath.Rel(root, full)
		if e != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("manifest path escapes repository: %q", name)
		}
		cur := root
		for _, part := range strings.Split(filepath.FromSlash(name), string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			info, e := os.Lstat(cur)
			if e != nil {
				return fmt.Errorf("manifest file %q: %w", name, e)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("manifest path %q contains a symlink", name)
			}
		}
		info, e := os.Stat(full)
		if e != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", name)
		}
	}
	return nil
}
func readObservations(dir string) ([]balance.Observation, error) {
	var obs []balance.Observation
	var paths []string
	var bytes int64
	e := filepath.WalkDir(dir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("history input %q must be a regular file", path)
		}
		bytes += info.Size()
		if bytes > 128<<20 {
			return fmt.Errorf("combined timing history exceeds the 128 MiB input limit")
		}
		paths = append(paths, path)
		return nil
	})
	if e != nil {
		return nil, e
	}
	for _, path := range paths {
		f, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		o, e := balance.ReadObservation(f)
		f.Close()
		if e != nil {
			return nil, e
		}
		obs = append(obs, o)
	}
	return obs, nil
}
func writeJSON(path string, v any) error {
	data, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(data)+1 > 8<<20 {
		return fmt.Errorf("JSON output exceeds the 8 MiB limit")
	}
	return writeBytes(path, append(data, '\n'))
}
func writeBytes(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, e := os.CreateTemp(dir, ".suite-balance-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if _, e = tmp.Write(data); e != nil {
		return e
	}
	if e = tmp.Sync(); e != nil {
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	ok = true
	return nil
}
func removeIfExists(p string) error {
	err := os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return canonicalPath(a) == canonicalPath(b)
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	parent := filepath.Dir(absolute)
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		parent = resolved
	}
	return filepath.Join(parent, filepath.Base(absolute))
}
func trailingNewline(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return "\n"
}
func matrix(p balance.Plan) map[string][]int {
	m := map[string][]int{"shard": {}}
	for index, s := range p.Allocation.Shards {
		if len(s.Files) > 0 {
			m["shard"] = append(m["shard"], index)
		}
	}
	return m
}
