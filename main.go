package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

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

func main() {
	var suitePaths stringSlice
	var headerFlags stringSlice
	var verbose bool
	var failFast bool
	var endpoint string

	flag.Var(&suitePaths, "test_suite_path", "Path to a test suite directory (can be specified multiple times)")
	flag.BoolVar(&verbose, "verbose", false, "Print detailed output including response diffs")
	flag.BoolVar(&failFast, "fail-fast", false, "Stop execution on first test failure")
	flag.StringVar(&endpoint, "endpoint", "", "External GraphQL endpoint URL (skips container creation)")
	flag.Var(&headerFlags, "header", "Custom header in 'Key: Value' format (can be specified multiple times)")
	flag.Parse()

	if len(suitePaths) == 0 {
		fmt.Fprintln(os.Stderr, "Error: at least one --test_suite_path is required")
		flag.Usage()
		os.Exit(1)
	}

	// Parse custom headers
	headers, err := parseHeaders(headerFlags)
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

	totalPassed := 0
	totalFailed := 0
	totalSkipped := 0

	// Run each test suite
	for _, suitePath := range suitePaths {
		var graphQLEndpoint string
		var container *runner.TwispContainer

		if useExternalEndpoint {
			graphQLEndpoint = endpoint
			fmt.Printf("\n========================================\n")
			fmt.Printf("Running suite against external endpoint: %s\n", suitePath)
			fmt.Printf("========================================\n")
			fmt.Printf("Endpoint: %s\n", graphQLEndpoint)
		} else {
			fmt.Printf("\n========================================\n")
			fmt.Printf("Starting container for suite: %s\n", suitePath)
			fmt.Printf("========================================\n")

			var err error
			container, err = runner.StartTwispContainer(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error starting container: %v\n", err)
				os.Exit(1)
			}
			graphQLEndpoint = container.GraphQLURL
			fmt.Printf("Container ready at: %s\n", graphQLEndpoint)
		}

		accountID := hashSuitePath(suitePath)
		r := runner.NewRunner(graphQLEndpoint, options, accountID, headers)
		result, err := r.RunSuite(ctx, suitePath)

		// Terminate container if we created one
		if container != nil {
			if termErr := container.Terminate(ctx); termErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to terminate container: %v\n", termErr)
			}
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
