package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/twisp/test-runner/runner"
)

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string {
	return fmt.Sprintf("%v", *s)
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// hashSuitePath returns a SHA256 hash of the suite path for use as account ID.
func hashSuitePath(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])
}

// parseHeaders parses header flags in "Key: Value" format into a map.
func parseHeaders(headerFlags []string) (map[string]string, error) {
	headers := make(map[string]string)
	for _, h := range headerFlags {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid header format: %q (expected 'Key: Value')", h)
		}
		headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return headers, nil
}

// hasGlobMeta reports whether s contains any shell-style glob metacharacters.
func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// resolveGlobs expands any glob patterns in paths, keeping non-glob entries
// as-is. Glob matches are restricted to directories.
func resolveGlobs(paths []string) ([]string, error) {
	var resolved []string
	seen := make(map[string]struct{})
	for _, p := range paths {
		if !hasGlobMeta(p) {
			if _, ok := seen[p]; !ok {
				seen[p] = struct{}{}
				resolved = append(resolved, p)
			}
			continue
		}
		matches, err := filepath.Glob(p)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", p, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("glob pattern %q matched nothing", p)
		}
		sort.Strings(matches)
		matched := 0
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || !info.IsDir() {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			resolved = append(resolved, m)
			matched++
		}
		if matched == 0 {
			return nil, fmt.Errorf("glob pattern %q matched no directories", p)
		}
	}
	return resolved, nil
}

func expandSuitePaths(paths []string) ([]string, error) {
	resolvedPaths, err := resolveGlobs(paths)
	if err != nil {
		return nil, err
	}

	var expanded []string
	seen := make(map[string]struct{})
	for _, suitePath := range resolvedPaths {
		info, err := os.Stat(suitePath)
		if err != nil {
			return nil, fmt.Errorf("invalid --test_suite_path %q: %w", suitePath, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("test suite path %q is not a directory", suitePath)
		}

		suites, err := runner.DiscoverTests(suitePath)
		if err != nil {
			return nil, fmt.Errorf("failed to discover suites under %q: %w", suitePath, err)
		}
		runnable := suites.RunnableSuitePaths()
		if len(runnable) == 0 {
			return nil, fmt.Errorf("no test suites found under %q", suitePath)
		}
		for _, relPath := range runnable {
			absPath := suitePath
			if relPath != "" {
				absPath = filepath.Join(suitePath, relPath)
			}
			if _, ok := seen[absPath]; ok {
				continue
			}
			seen[absPath] = struct{}{}
			expanded = append(expanded, absPath)
		}
	}
	if len(expanded) == 0 {
		return nil, fmt.Errorf("no test suites found")
	}
	return expanded, nil
}

func main() {
	var suitePaths stringSlice
	var headerFlags stringSlice
	var verbose bool
	var failFast bool
	var endpoint string
	var image string
	var pull bool
	var parallel int
	var summary bool

	flag.Var(&suitePaths, "test_suite_path", "Path to a test suite directory (can be specified multiple times)")
	flag.BoolVar(&verbose, "verbose", false, "Print detailed output including response diffs")
	flag.BoolVar(&failFast, "fail-fast", false, "Stop execution on first test failure")
	flag.StringVar(&endpoint, "endpoint", "", "External GraphQL endpoint URL (skips container creation)")
	flag.StringVar(&image, "image", runner.TwispImage, "Fully qualified Docker image to use for local container")
	flag.BoolVar(&pull, "pull", false, "Always pull the container image before starting")
	flag.Var(&headerFlags, "header", "Custom header in 'Key: Value' format (can be specified multiple times)")
	flag.IntVar(&parallel, "parallel", 1, "Number of test suites to run concurrently against the shared endpoint (each suite uses a unique account ID)")
	flag.BoolVar(&summary, "summary", false, "Suppress per-suite output; print only the final summary, runtimes, and any failures")

	// Parse iteratively so we can sweep up positional args between flags.
	// This lets unquoted shell globs work for --test_suite_path (the shell
	// expands to many args; flag only consumes one, the rest are positional)
	// without forcing the user to put --test_suite_path last on the line.
	args := os.Args[1:]
	for {
		if err := flag.CommandLine.Parse(args); err != nil {
			os.Exit(2)
		}
		positional := flag.Args()
		if len(positional) == 0 {
			break
		}
		suitePaths = append(suitePaths, positional[0])
		args = positional[1:]
	}

	if len(suitePaths) == 0 {
		fmt.Fprintln(os.Stderr, "Error: at least one --test_suite_path is required")
		flag.Usage()
		os.Exit(1)
	}

	if parallel < 1 {
		parallel = 1
	}

	// Parse custom headers
	headers, err := parseHeaders(headerFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	expandedSuitePaths, err := expandSuitePaths([]string(suitePaths))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	useExternalEndpoint := endpoint != ""

	// Set up context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nReceived interrupt signal, shutting down...")
		cancel()
	}()

	options := runner.Options{
		Verbose:  verbose,
		FailFast: failFast,
	}

	if parallel > len(expandedSuitePaths) {
		parallel = len(expandedSuitePaths)
	}
	buffered := parallel > 1

	runStart := time.Now()

	// Start a single shared endpoint for all suites. Each suite uses a unique
	// account ID (tenant), so they don't collide on the server.
	var graphQLEndpoint string
	if useExternalEndpoint {
		graphQLEndpoint = endpoint
		fmt.Printf("\n========================================\n")
		fmt.Printf("Using external endpoint for %d suite(s)\n", len(expandedSuitePaths))
		fmt.Printf("========================================\n")
		fmt.Printf("Endpoint: %s\n", graphQLEndpoint)
	} else {
		fmt.Printf("\n========================================\n")
		fmt.Printf("Starting shared container for %d suite(s)\n", len(expandedSuitePaths))
		fmt.Printf("========================================\n")

		container, err := runner.StartTwispContainer(ctx, image, pull)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error starting container: %v\n", err)
			os.Exit(1)
		}
		// Use Background on terminate so a cancelled parent ctx still cleans up.
		defer func() {
			if termErr := container.Terminate(context.Background()); termErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to terminate container: %v\n", termErr)
			}
		}()
		graphQLEndpoint = container.GraphQLURL
		fmt.Printf("Container ready at: %s\n", graphQLEndpoint)
	}

	type testTiming struct {
		path     string
		duration time.Duration
		passed   bool
		errMsg   string
	}

	type suiteOutcome struct {
		path                    string
		passed, failed, skipped int
		duration                time.Duration
		tests                   []testTiming
		runErr                  error
	}

	jobs := make(chan string)
	results := make(chan suiteOutcome, len(expandedSuitePaths))
	var stdoutMu sync.Mutex
	var wg sync.WaitGroup

	flush := func(buf *bytes.Buffer) {
		if buf == nil {
			return
		}
		stdoutMu.Lock()
		defer stdoutMu.Unlock()
		os.Stdout.Write(buf.Bytes())
	}

	worker := func() {
		defer wg.Done()
		for suitePath := range jobs {
			if ctx.Err() != nil {
				results <- suiteOutcome{}
				continue
			}

			var buf *bytes.Buffer
			if buffered && !summary {
				buf = &bytes.Buffer{}
			}

			accountID := hashSuitePath(suitePath)
			r := runner.NewRunner(graphQLEndpoint, options, accountID, headers)
			switch {
			case summary:
				r.SetOutput(io.Discard)
			case buffered:
				r.SetOutput(buf)
			}
			result, err := r.RunSuite(ctx, suitePath)

			flush(buf)

			if err != nil {
				results <- suiteOutcome{path: suitePath, runErr: fmt.Errorf("running suite %q: %w", suitePath, err)}
				continue
			}

			tests := make([]testTiming, 0, len(result.Results))
			for _, tr := range result.Results {
				name := suitePath
				if tr.Test != nil && tr.Test.Dir != "" {
					name = filepath.Join(suitePath, tr.Test.Dir)
				}
				var errMsg string
				if tr.Error != nil {
					errMsg = tr.Error.Error()
				}
				tests = append(tests, testTiming{
					path:     name,
					duration: tr.Duration,
					passed:   tr.Passed,
					errMsg:   errMsg,
				})
			}

			results <- suiteOutcome{
				path:     suitePath,
				passed:   result.Passed,
				failed:   result.Failed,
				skipped:  result.Skipped,
				duration: result.Duration,
				tests:    tests,
			}

			if failFast && result.Failed > 0 {
				cancel()
			}
		}
	}

	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go worker()
	}

	go func() {
		defer close(jobs)
		for _, suitePath := range expandedSuitePaths {
			select {
			case <-ctx.Done():
				return
			case jobs <- suitePath:
			}
		}
	}()

	wg.Wait()
	close(results)

	totalPassed := 0
	totalFailed := 0
	totalSkipped := 0
	var firstRunErr error
	var collectedSuites []suiteOutcome
	var allTests []testTiming
	for outcome := range results {
		if outcome.runErr != nil && firstRunErr == nil {
			firstRunErr = outcome.runErr
		}
		totalPassed += outcome.passed
		totalFailed += outcome.failed
		totalSkipped += outcome.skipped
		if outcome.path != "" {
			collectedSuites = append(collectedSuites, outcome)
		}
		allTests = append(allTests, outcome.tests...)
	}

	wallTime := time.Since(runStart)

	if firstRunErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", firstRunErr)
		os.Exit(1)
	}

	// Failures, if any. Useful especially in --summary mode where per-test
	// FAIL output is suppressed during the run.
	var failures []testTiming
	for _, t := range allTests {
		if !t.passed {
			failures = append(failures, t)
		}
	}
	if len(failures) > 0 {
		fmt.Printf("\n========================================\n")
		fmt.Printf("Failures (%d)\n", len(failures))
		fmt.Printf("========================================\n")
		for _, f := range failures {
			fmt.Printf("  FAIL  %s\n", f.path)
			if f.errMsg != "" {
				fmt.Printf("        %s\n", f.errMsg)
			}
		}
	}

	// Per-suite timings, slowest first.
	if len(collectedSuites) > 0 {
		sort.Slice(collectedSuites, func(i, j int) bool {
			return collectedSuites[i].duration > collectedSuites[j].duration
		})
		fmt.Printf("\n========================================\n")
		fmt.Printf("Suite runtimes (slowest first)\n")
		fmt.Printf("========================================\n")
		var suiteSum time.Duration
		for _, s := range collectedSuites {
			suiteSum += s.duration
			fmt.Printf("  %10s  %s\n", s.duration.Round(time.Millisecond), s.path)
		}
		fmt.Printf("  %10s  (sum of suite durations)\n", suiteSum.Round(time.Millisecond))
	}

	// Slowest individual tests (top 10) when there's enough material to rank.
	if len(allTests) > 1 {
		sort.Slice(allTests, func(i, j int) bool {
			return allTests[i].duration > allTests[j].duration
		})
		topN := min(10, len(allTests))
		fmt.Printf("\nSlowest tests (top %d of %d)\n", topN, len(allTests))
		for _, t := range allTests[:topN] {
			status := "PASS"
			if !t.passed {
				status = "FAIL"
			}
			fmt.Printf("  %10s  %s  %s\n", t.duration.Round(time.Millisecond), status, t.path)
		}
	}

	// Print summary
	fmt.Printf("\n========================================\n")
	fmt.Printf("TOTAL: %d passed, %d failed, %d skipped\n", totalPassed, totalFailed, totalSkipped)
	fmt.Printf("Wall time: %v  (parallel=%d)\n", wallTime.Round(time.Millisecond), parallel)
	fmt.Printf("========================================\n")

	if totalFailed > 0 {
		os.Exit(1)
	}
}
