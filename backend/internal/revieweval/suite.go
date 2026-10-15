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

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
