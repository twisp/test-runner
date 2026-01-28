package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Test represents a single test case with its associated files.
type Test struct {
	Name      string // Test name (directory name)
	Dir       string // Relative directory path
	AbsDir    string // Absolute directory path
	Seq       int    // Sequence number for ordering (-1 if not sequenced)
	Request   string // Path to request.gql
	Response  string // Path to response.json
	Variables string // Path to variables.json (optional)
	Transform string // Path to transform.jq (optional)
}

// Suite represents a test suite with a base test and child tests.
type Suite struct {
	Path     string            // Relative path of the suite
	Base     *Test             // Base test for this suite (may be nil)
	Tests    map[string]string // Map of test name to child suite path
	Children map[string]*Suite // Child suites
	refs     int               // Reference count (internal)
}

// Suites is a collection of test suites indexed by path.
type Suites map[string]*Suite

// DiscoverTests walks the given directory and discovers all test fixtures.
func DiscoverTests(suitePath string) (Suites, error) {
	absPath, err := filepath.Abs(suitePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	suites := make(Suites)

	err = filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(absPath, path)
		if err != nil {
			return err
		}

		// Handle root directory
		if relPath == "." {
			relPath = ""
		}

		if info.IsDir() {
			suites[relPath] = &Suite{
				Path:     relPath,
				Tests:    make(map[string]string),
				Children: make(map[string]*Suite),
			}
			return nil
		}

		test, isTest := parseTestFile(absPath, path, info)
		if !isTest {
			return nil
		}

		suite, ok := suites[test.Dir]
		if !ok {
			return fmt.Errorf("suite for '%s' not found", test.Dir)
		}

		isNew := suite.Base == nil
		if isNew {
			suite.Base = test
		} else {
			suite.Base = mergeTest(suite.Base, test)
		}

		parentPath := getParentPath(suite.Path)
		if suite.Path != "" {
			parent, ok := suites[parentPath]
			if !ok {
				return fmt.Errorf("expected parent path '%s' for '%s'", parentPath, suite.Path)
			}

			if isNew {
				parent.refs++
			}

			if suite.Base.Seq >= 0 {
				parent.Tests[test.Name] = suite.Path
				parent.Children[test.Name] = suite
				if isNew {
					suite.refs++
				}
			}

			suites[parentPath] = parent
		}

		suites[test.Dir] = suite

		return nil
	})
	if err != nil {
		return nil, err
	}

	for path, suite := range suites {
		if suite.Base == nil && len(suite.Tests) == 0 {
			delete(suites, path)
		}
	}

	return suites, nil
}

// GetOrderedTests returns all tests from the suite in execution order.
func (s Suites) GetOrderedTests(suitePath string) []*Test {
	var tests []*Test
	s.run(func(t *Test) {
		tests = append(tests, t)
	}, suitePath, -1)
	return tests
}

// RunnableSuitePaths returns suite paths that should be executed directly.
// Suites referenced by a parent (refs > 0) with no child tests are skipped.
func (s Suites) RunnableSuitePaths() []string {
	paths := make([]string, 0, len(s))
	for path, suite := range s {
		if suite.refs > 0 && len(suite.Tests) == 0 {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// run executes fn for each test in order, respecting dependencies.
// maxSeq limits which sequenced child tests to include (-1 for all).
func (s Suites) run(fn func(*Test), suitePath string, maxSeq int) {
	// Track what we've already run to avoid duplicates
	visited := make(map[string]bool)
	s.runWithVisited(fn, suitePath, maxSeq, visited)
}

func (s Suites) runWithVisited(fn func(*Test), suitePath string, maxSeq int, visited map[string]bool) {
	if visited[suitePath] {
		return
	}

	suite, ok := s[suitePath]
	if !ok {
		return
	}

	// Handle parent suite first (run all parent tests)
	if parentPath := getParentPath(suitePath); parentPath != "" {
		s.runWithVisited(fn, parentPath, -1, visited)
	}

	// Mark as visited before running to prevent re-entry
	visited[suitePath] = true

	// Run base test if present and valid
	if suite.Base != nil && suite.Base.IsValid() {
		fn(suite.Base)
	}

	// Collect child tests
	var childTests []*Test
	for _, childPath := range suite.Tests {
		child, ok := s[childPath]
		if !ok || child.Base == nil || !child.Base.IsValid() {
			continue
		}
		// For sequenced tests, respect maxSeq limit
		if maxSeq >= 0 && child.Base.Seq >= 0 && child.Base.Seq > maxSeq {
			continue
		}
		childTests = append(childTests, child.Base)
	}

	// Sort: sequenced tests first (by sequence), then non-sequenced (alphabetically)
	sort.Slice(childTests, func(i, j int) bool {
		// Both have sequence numbers
		if childTests[i].Seq >= 0 && childTests[j].Seq >= 0 {
			return childTests[i].Seq < childTests[j].Seq
		}
		// Sequenced tests come before non-sequenced
		if childTests[i].Seq >= 0 {
			return true
		}
		if childTests[j].Seq >= 0 {
			return false
		}
		// Both non-sequenced: sort alphabetically
		return childTests[i].Name < childTests[j].Name
	})

	for _, test := range childTests {
		fn(test)
	}
}

// parseTestFile extracts test information from a file path.
func parseTestFile(basePath, fullPath string, info os.FileInfo) (*Test, bool) {
	if info.IsDir() {
		return nil, false
	}

	// Skip files in SKIP directories
	if strings.Contains(fullPath, "/SKIP") {
		return nil, false
	}

	relPath, err := filepath.Rel(basePath, fullPath)
	if err != nil {
		return nil, false
	}

	fileName := filepath.Base(relPath)
	dirPath := filepath.Dir(relPath)

	// Handle root level files
	if dirPath == "." {
		dirPath = ""
	}

	dirParts := strings.Split(dirPath, string(filepath.Separator))
	if len(dirParts) == 0 || (len(dirParts) == 1 && dirParts[0] == "") {
		// Root level - use the file's directory name
		dirParts = []string{""}
	}

	testName := dirParts[len(dirParts)-1]
	if testName == "" && len(dirParts) > 1 {
		testName = dirParts[len(dirParts)-2]
	}

	// Parse sequence number from directory name (e.g., "001_TestName" -> 1)
	seq := -1
	if parts := strings.SplitN(testName, "_", 2); len(parts) > 1 {
		if n, err := strconv.Atoi(parts[0]); err == nil {
			seq = n
		}
	}

	test := &Test{
		Name:   testName,
		Dir:    dirPath,
		AbsDir: filepath.Join(basePath, dirPath),
		Seq:    seq,
	}

	switch fileName {
	case "request.gql":
		test.Request = fullPath
	case "response.json":
		test.Response = fullPath
	case "variables.json":
		test.Variables = fullPath
	case "transform.jq":
		test.Transform = fullPath
	default:
		return nil, false
	}

	return test, true
}

// mergeTest combines two test objects, keeping non-empty values from src.
func mergeTest(dst, src *Test) *Test {
	if src.Request != "" {
		dst.Request = src.Request
	}
	if src.Response != "" {
		dst.Response = src.Response
	}
	if src.Variables != "" {
		dst.Variables = src.Variables
	}
	if src.Transform != "" {
		dst.Transform = src.Transform
	}
	return dst
}

// getParentPath returns the parent path of a suite path.
func getParentPath(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], string(filepath.Separator))
}

// IsValid returns true if the test has both request and response files.
func (t *Test) IsValid() bool {
	return t.Request != "" && t.Response != ""
}
