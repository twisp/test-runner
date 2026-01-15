package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Result represents the outcome of a single test.
type Result struct {
	Test     *Test
	Passed   bool
	Duration time.Duration
	Error    error
	Expected string
	Actual   string
}

// SuiteResult represents the outcome of running a test suite.
type SuiteResult struct {
	SuitePath string
	Results   []*Result
	Passed    int
	Failed    int
	Skipped   int
	Duration  time.Duration
}

// Options configures the test runner behavior.
type Options struct {
	Verbose  bool // Print detailed output
	FailFast bool // Stop on first failure
}

// Runner executes GraphQL tests against a Twisp endpoint.
type Runner struct {
	client    *GraphQLClient
	options   Options
	accountID string
}

// NewRunner creates a new test runner for the given GraphQL endpoint.
// Custom headers will be applied to all requests (overriding defaults if same key).
func NewRunner(endpoint string, options Options, accountID string, headers map[string]string) *Runner {
	return &Runner{
		client:    NewGraphQLClient(endpoint, accountID, headers),
		options:   options,
		accountID: accountID,
	}
}

// RunSuite executes all tests in the given suite path.
func (r *Runner) RunSuite(ctx context.Context, suitePath string) (*SuiteResult, error) {
	start := time.Now()

	suites, err := DiscoverTests(suitePath)
	if err != nil {
		return nil, fmt.Errorf("failed to discover tests: %w", err)
	}

	result := &SuiteResult{
		SuitePath: suitePath,
	}

	// Get ordered tests for the root suite
	tests := r.collectAllTests(suites)

	fmt.Printf("\n=== Running suite: %s ===\n", suitePath)
	fmt.Printf("Discovered %d tests\n\n", len(tests))

	for _, test := range tests {
		if !test.IsValid() {
			result.Skipped++
			if r.options.Verbose {
				fmt.Printf("SKIP: %s (missing request.gql or response.json)\n", test.Dir)
			}
			continue
		}

		testResult := r.RunTest(ctx, test)
		result.Results = append(result.Results, testResult)

		if testResult.Passed {
			result.Passed++
			fmt.Printf("PASS: %s (%v)\n", test.Dir, testResult.Duration.Round(time.Millisecond))
		} else {
			result.Failed++
			fmt.Printf("FAIL: %s (%v)\n", test.Dir, testResult.Duration.Round(time.Millisecond))
			if testResult.Error != nil {
				fmt.Printf("      Error: %v\n", testResult.Error)
			}
			if r.options.Verbose && testResult.Expected != "" && testResult.Actual != "" {
				fmt.Printf("      Expected: %s\n", compact(testResult.Expected))
				fmt.Printf("      Actual:   %s\n", compact(testResult.Actual))
			}

			if r.options.FailFast {
				break
			}
		}
	}

	result.Duration = time.Since(start)

	fmt.Printf("\n=== Suite complete: %d passed, %d failed, %d skipped (%v) ===\n",
		result.Passed, result.Failed, result.Skipped, result.Duration.Round(time.Millisecond))

	return result, nil
}

// RunTest executes a single test and returns the result.
func (r *Runner) RunTest(ctx context.Context, test *Test) *Result {
	start := time.Now()
	result := &Result{
		Test: test,
	}

	// Read request
	query, err := os.ReadFile(test.Request)
	if err != nil {
		result.Error = fmt.Errorf("failed to read request: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	// Read variables if present
	var variables map[string]any
	if test.Variables != "" {
		varsData, err := os.ReadFile(test.Variables)
		if err != nil {
			result.Error = fmt.Errorf("failed to read variables: %w", err)
			result.Duration = time.Since(start)
			return result
		}
		if err := json.Unmarshal(varsData, &variables); err != nil {
			result.Error = fmt.Errorf("failed to parse variables: %w", err)
			result.Duration = time.Since(start)
			return result
		}
	}

	// Execute request
	actualJSON, err := r.client.Execute(ctx, string(query), variables)
	if err != nil {
		result.Error = fmt.Errorf("failed to execute request: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	// Apply transforms to actual response
	if test.Transform != "" {
		actualJSON, err = TransformJSON(test.Transform, actualJSON)
		if err != nil {
			result.Error = fmt.Errorf("failed to transform actual response: %w", err)
			result.Duration = time.Since(start)
			return result
		}
	}

	// Read expected response
	expectedJSON, err := os.ReadFile(test.Response)
	if err != nil {
		result.Error = fmt.Errorf("failed to read expected response: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	// Apply transforms to expected response
	if test.Transform != "" {
		expectedJSON, err = TransformJSON(test.Transform, expectedJSON)
		if err != nil {
			result.Error = fmt.Errorf("failed to transform expected response: %w", err)
			result.Duration = time.Since(start)
			return result
		}
	}

	// Compare JSON
	result.Expected = string(expectedJSON)
	result.Actual = string(actualJSON)
	result.Passed = jsonEqual(expectedJSON, actualJSON)
	result.Duration = time.Since(start)

	if !result.Passed && result.Error == nil {
		result.Error = fmt.Errorf("response mismatch")
	}

	return result
}

// collectAllTests returns all valid tests from all suites in proper execution order.
// The order is: root base test first, then child tests sorted by sequence number.
func (r *Runner) collectAllTests(suites Suites) []*Test {
	// Get ordered tests starting from the root suite
	return suites.GetOrderedTests("")
}

// jsonEqual compares two JSON byte slices for semantic equality.
func jsonEqual(a, b []byte) bool {
	var aVal, bVal any
	if err := json.Unmarshal(a, &aVal); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bVal); err != nil {
		return false
	}

	// Re-marshal to normalize
	aNorm, err := json.Marshal(aVal)
	if err != nil {
		return false
	}
	bNorm, err := json.Marshal(bVal)
	if err != nil {
		return false
	}

	return string(aNorm) == string(bNorm)
}

// truncate shortens a string to the given length.
func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func compact(s string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return strings.ReplaceAll(s, "\n", " ")
	}
	return buf.String()
}
