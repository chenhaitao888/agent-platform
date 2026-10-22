// Package revieweval records repeatable synthetic reviews and scores explicit
// human adjudications. A successful empty report and an execution failure remain
// different outcomes; neither can hide a planted issue from recall.
package revieweval

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed suite.json
var suiteJSON []byte

//go:embed suite-extended-v1.json
var extendedSuiteJSON []byte

type Suite struct {
	SchemaVersion string `json:"schemaVersion"`
	Name          string `json:"name"`
	Cases         []Case `json:"cases"`
}

type Case struct {
	ID       string            `json:"id"`
	Category string            `json:"category"`
	Base     map[string]string `json:"base"`
	Head     map[string]string `json:"head"`
	Issues   []Issue           `json:"issues"`
}

type Issue struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Rationale string `json:"rationale"`
}

// DefaultSuite returns a fresh copy. Ground truth never enters the worktree,
// patch, or prompt supplied to the model.
func DefaultSuite() Suite {
	var suite Suite
	if err := json.Unmarshal(suiteJSON, &suite); err != nil {
		panic("invalid embedded evaluation suite")
	}
	return suite
}

func SuiteSHA256() string { return digest(suiteJSON) }

// LoadSuite selects reviewed, embedded input only. The default stays pinned to
// the original six cases; adding a suite cannot invalidate historical captures.
// The digest identifies the exact source bytes, not a re-serialized export.
func LoadSuite(name string) (Suite, string, error) {
	var data []byte
	switch name {
	case "", "starter-v1":
		data = suiteJSON
	case "extended-v1":
		data = extendedSuiteJSON
	default:
		return Suite{}, "", ErrInvalidEvaluation
	}
	var suite Suite
	if json.Unmarshal(data, &suite) != nil {
		return Suite{}, "", ErrInvalidEvaluation
	}
	return suite, digest(data), nil
}

func suiteForDigest(expected string) (Suite, error) {
	for _, name := range []string{"starter-v1", "extended-v1"} {
		suite, sum, err := LoadSuite(name)
		if err == nil && sum == expected {
			return suite, nil
		}
	}
	return Suite{}, ErrInvalidEvaluation
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
