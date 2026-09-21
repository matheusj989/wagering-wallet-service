package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// declared finds every variable the loader reads. Reading the source keeps this
// list honest: a variable added to config.go shows up here without anyone
// remembering to update a list by hand.
var declared = regexp.MustCompile(`r\.(?:text|oneOf|link|dsn|number|count|duration|flag)\("([A-Z0-9_]+)"`)

func repositoryRoot(t *testing.T) string {
	t.Helper()

	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("the working directory could not be read: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("go.mod was not found above the test directory")
		}
		directory = parent
	}
}

func readFile(t *testing.T, path ...string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatalf("file %s could not be read: %v", filepath.Join(path...), err)
	}
	return string(content)
}

func variablesRead(t *testing.T, root string) []string {
	t.Helper()

	source := readFile(t, root, "internal", "infrastructure", "config", "config.go")
	seen := map[string]bool{}
	for _, match := range declared.FindAllStringSubmatch(source, -1) {
		seen[match[1]] = true
	}
	if len(seen) == 0 {
		t.Fatal("no variable was found in config.go, the pattern is stale")
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestEnvironmentFiles(t *testing.T) {
	root := repositoryRoot(t)
	names := variablesRead(t, root)

	t.Run("Given the variables the service reads/When .env.example is checked/Then every one of them is documented", func(t *testing.T) {
		// Given
		example := readFile(t, root, ".env.example")

		// When, Then
		for _, name := range names {
			if !regexp.MustCompile(`(?m)^` + name + `=`).MatchString(example) {
				t.Errorf("%s is read by the service and missing from .env.example", name)
			}
		}
	})

	t.Run("Given the variables the service reads/When the compose service is checked/Then every one of them is passed to the container", func(t *testing.T) {
		// Given
		service := readFile(t, root, "deployments", "app", "app.yml")

		// When, Then
		for _, name := range names {
			if !regexp.MustCompile(`(?m)^\s+` + name + `:`).MatchString(service) {
				t.Errorf("%s is read by the service and missing from deployments/app/app.yml", name)
			}
		}
	})
}
