package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/itchyny/gojq"
)

// TransformJSON applies JQ transforms from the given transform file to the JSON data.
// Each line in the transform file is treated as a separate JQ filter.
// Filters are applied sequentially, with each filter receiving the output of the previous.
func TransformJSON(transformFile string, jsonData []byte) ([]byte, error) {
	if transformFile == "" {
		return jsonData, nil
	}

	// Read transform filters from file
	transforms, err := readTransformFile(transformFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read transform file: %w", err)
	}

	if len(transforms) == 0 {
		return jsonData, nil
	}

	// Parse JSON into a generic structure
	var data any
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Apply each transform sequentially
	for _, xform := range transforms {
		if xform == "" {
			continue
		}

		query, err := gojq.Parse(xform)
		if err != nil {
			return nil, fmt.Errorf("failed to parse jq expression '%s': %w", xform, err)
		}

		iter := query.Run(data)
		val, ok := iter.Next()
		if !ok {
			return nil, fmt.Errorf("jq expression '%s' produced no output", xform)
		}

		if err, isErr := val.(error); isErr {
			return nil, fmt.Errorf("jq expression '%s' failed: %w", xform, err)
		}

		data = val
	}

	// Marshal back to JSON
	result, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transformed JSON: %w", err)
	}

	return result, nil
}

// readTransformFile reads a transform file and returns the list of JQ filters.
func readTransformFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var transforms []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Skip empty lines and comments
		if line != "" && line[0] != '#' {
			transforms = append(transforms, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return transforms, nil
}
