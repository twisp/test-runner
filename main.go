package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/twisp/runner/runner"
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

func main() {
	var suitePaths stringSlice
	var verbose bool
	var failFast bool

	flag.Var(&suitePaths, "test_suite_path", "Path to a test suite directory (can be specified multiple times)")
	flag.BoolVar(&verbose, "verbose", false, "Print detailed output including response diffs")
	flag.BoolVar(&failFast, "fail-fast", false, "Stop execution on first test failure")
	flag.Parse()

	if len(suitePaths) == 0 {
		fmt.Fprintln(os.Stderr, "Error: at least one --test_suite_path is required")
		flag.Usage()
		os.Exit(1)
	}

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

	totalPassed := 0
	totalFailed := 0
	totalSkipped := 0

	// Run each test suite with its own container
	for _, suitePath := range suitePaths {
		fmt.Printf("\n========================================\n")
		fmt.Printf("Starting container for suite: %s\n", suitePath)
		fmt.Printf("========================================\n")

		container, err := runner.StartTwispContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error starting container: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Container ready at: %s\n", container.GraphQLURL)

		accountID := hashSuitePath(suitePath)
		r := runner.NewRunner(container, options, accountID)
		result, err := r.RunSuite(ctx, suitePath)

		// Always try to terminate the container
		if termErr := container.Terminate(ctx); termErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to terminate container: %v\n", termErr)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error running suite: %v\n", err)
			os.Exit(1)
		}

		totalPassed += result.Passed
		totalFailed += result.Failed
		totalSkipped += result.Skipped

		// Exit early if fail-fast and there were failures
		if failFast && result.Failed > 0 {
			break
		}
	}

	// Print summary
	fmt.Printf("\n========================================\n")
	fmt.Printf("TOTAL: %d passed, %d failed, %d skipped\n", totalPassed, totalFailed, totalSkipped)
	fmt.Printf("========================================\n")

	if totalFailed > 0 {
		os.Exit(1)
	}
}
