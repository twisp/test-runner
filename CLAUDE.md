# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Twisp GraphQL Test Runner - a CLI tool for running GraphQL test fixtures against the Twisp local container using testcontainers-go.

## Build and Run Commands

```bash
# Build the binary
go build -o test-runner

# Run tests against a fixture directory (uses testcontainers)
./test-runner --test_suite_path /path/to/fixtures

# Run against an external endpoint (skips container creation)
./test-runner --endpoint http://localhost:8080/financial/v1/graphql --test_suite_path /path/to/fixtures

# Run with custom headers (useful for auth tokens)
./test-runner --endpoint http://api.example.com/graphql \
  --header "Authorization: Bearer token" \
  --header "X-Custom-Header: value" \
  --test_suite_path /path/to/fixtures

# Run with verbose output (shows response diffs)
./test-runner --test_suite_path /path/to/fixtures --verbose

# Run multiple suites (each gets its own container)
./test-runner --test_suite_path /path/to/fixtures/errors --test_suite_path /path/to/fixtures/workflow

# Stop on first failure
./test-runner --test_suite_path /path/to/fixtures --fail-fast
```

## Architecture

The project follows a simple structure with the main entry point and a `runner` package:

- `main.go` - CLI entry point, handles flags and orchestrates test suite execution
- `runner/container.go` - Manages testcontainer lifecycle for `public.ecr.aws/twisp/local:latest` (override with `--image`)
- `runner/client.go` - HTTP client for GraphQL requests
- `runner/discovery.go` - Walks directories to find test fixtures, handles test sequencing
- `runner/runner.go` - Core test execution logic, compares expected vs actual responses
- `runner/transform.go` - JQ transform support using gojq library

### Test Discovery and Execution Flow

1. Each `--test_suite_path` gets its own isolated container (or uses `--endpoint` if provided)
2. `DiscoverTests()` walks the directory tree building a `Suites` map
3. Tests are ordered by sequence prefix (e.g., `001_`, `002_`) then alphabetically
4. For each test: read `request.gql`, optionally `variables.json`, execute against endpoint
5. Apply `transform.jq` filters (if present) to both actual and expected responses
6. Compare JSON semantically (order-independent)
7. Custom `--header` flags are applied to all requests, overriding defaults

### Test Fixture Structure

```
my-suite/
├── request.gql       # GraphQL query (required)
├── response.json     # Expected response (required)
├── variables.json    # Query variables (optional)
├── transform.jq      # JQ filters to normalize response (optional)
└── 001_FirstTest/    # Sequenced child test
    └── ...
```

Directories containing `SKIP` in the path are ignored.

## Dependencies

- Docker must be running (required by testcontainers-go, not needed when using `--endpoint`)
- Access to `public.ecr.aws/twisp/local:latest` image (not needed when using `--endpoint`, override with `--image`)
