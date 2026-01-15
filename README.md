# Twisp GraphQL Test Runner

A standalone CLI tool for running GraphQL test fixtures against the Twisp local container using testcontainers-go.

## Installation

```bash
go install github.com/twisp/test-runner@latest
```

Or build from source:

```bash
git clone https://github.com/twisp/test-runner.git
cd test-runner
go build -o test-runner
```

## Usage

```bash
# Run the example book-transfer suite (uses testcontainers)
./test-runner --test_suite_path ./example-suites/book-transfer

# Run against an external endpoint (skips container creation)
./test-runner \
  --endpoint http://localhost:8080/financial/v1/graphql \
  --test_suite_path /path/to/fixtures

# Run with custom headers (useful for auth tokens)
./test-runner \
  --endpoint https://api.us-east-1.cloud.twisp.com/financial/v1/graphql \
  --header "Authorization: Bearer token" \
  --header "X-Request-Id: test-123" \
  --test_suite_path /path/to/fixtures

# Run multiple test suites (each gets its own container)
./test-runner \
  --test_suite_path /path/to/fixtures/errors \
  --test_suite_path /path/to/fixtures/transferWorkflow

# With options
./test-runner --test_suite_path /path/to/fixtures --verbose --fail-fast
```

### Options

| Flag | Description |
|------|-------------|
| `--test_suite_path` | Path to a test suite directory (required, can be repeated) |
| `--endpoint` | External GraphQL endpoint URL (skips container creation) |
| `--header` | Custom header in `Key: Value` format (can be repeated, overrides defaults) |
| `--verbose` | Print detailed output including response diffs |
| `--fail-fast` | Stop execution on first test failure |

## Test Fixture Format

Test fixtures are organized in directories with the following structure:

```
my-test-suite/
├── request.gql           # GraphQL query/mutation (required)
├── response.json         # Expected response (required)
├── variables.json        # Variables for the query (optional)
├── transform.jq          # JQ transform to normalize response (optional)
└── 001_FirstTest/        # Sequenced child test
    ├── request.gql
    ├── response.json
    └── 002_SecondTest/   # Nested sequenced test
        ├── request.gql
        └── response.json
```

### Test Sequencing

- Tests are executed in sequence order based on directory name prefixes (e.g., `001_`, `002_`)
- The base test (at suite root) runs first, followed by child tests in sequence
- Tests without sequence prefixes run after sequenced tests, sorted alphabetically
- Directories containing `SKIP` in the path are ignored

### JQ Transforms

The `transform.jq` file contains JQ expressions (one per line) that normalize both actual and expected responses before comparison. This is useful for removing dynamic fields like timestamps or IDs.

Example `transform.jq`:
```jq
walk(if type == "object" then with_entries(select(.key | test("created|modified") | not)) else . end)
```

## How It Works

1. For each `--test_suite_path`, the runner:
   - Starts a fresh `public.ecr.aws/twisp/local:latest` container (or uses `--endpoint` if provided)
   - Discovers all test fixtures in the directory tree
   - Executes tests in order (setup first, then children by sequence)
   - Compares actual responses against expected responses
   - Terminates the container (if one was started)

2. Each test suite gets its own isolated container for clean state (when not using `--endpoint`)

3. Test results are reported with pass/fail status and timing

4. Custom headers via `--header` are applied to all requests, overriding defaults like `X-Twisp-Account-Id`

## Requirements

- Go 1.21+
- Docker (for testcontainers, not required when using `--endpoint`)
- Access to `public.ecr.aws/twisp/local:latest` image (not required when using `--endpoint`)

## Example Output

```
========================================
Starting container for suite: /path/to/fixtures/errors
========================================
Container ready at: http://localhost:32770/financial/v1/graphql

=== Running suite: /path/to/fixtures/errors ===
Discovered 13 tests

PASS:  (636ms)
PASS: BAD_REQUEST.eqRequiredForPartitionKey (2ms)
PASS: BAD_REQUEST.partitionKeyRequired (1ms)
PASS: DATE_PARSE_ERROR (15ms)
...

=== Suite complete: 12 passed, 1 failed, 0 skipped (822ms) ===

========================================
TOTAL: 12 passed, 1 failed, 0 skipped
========================================
```

## Project Structure

```
.
├── main.go              # CLI entrypoint
├── runner/
│   ├── container.go     # Testcontainer management
│   ├── client.go        # GraphQL HTTP client
│   ├── discovery.go     # Test fixture discovery
│   ├── transform.go     # JQ transform support
│   └── runner.go        # Core test execution
├── go.mod
└── go.sum
```

## License

MIT License - see [LICENSE](LICENSE) for details.
