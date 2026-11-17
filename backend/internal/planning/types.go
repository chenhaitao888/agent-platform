// Package planning builds read-only, snapshot-bound proposals. It deliberately
// has no Apply, activation, approval or external write interface.
package planning

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid planning input")
	ErrUnavailable = errors.New("planning source unavailable")
)
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)

func ValidIdentifier(value string) bool {
	return identifier.MatchString(value) && !strings.Contains(value, "..") && !strings.Contains(value, "://")
}

type ResourceBinding struct {
	ConnectorRef string `json:"connectorRef"`
	ResourceRef  string `json:"resourceRef"`
}
type EnvironmentBinding struct {
	Environment string           `json:"environment"`
	Config      *ResourceBinding `json:"config,omitempty"`
	Experiment  *ResourceBinding `json:"experiment,omitempty"`
}
type Target struct {
	TenantID     string `json:"tenantId"`
	Environment  string `json:"environment"`
	ConnectorRef string `json:"connectorRef"`
	ResourceRef  string `json:"resourceRef"`
}
type Descriptor struct {
	Kind                  string `json:"kind"`
	ContractVersion       string `json:"contractVersion"`
	ImplementationVersion string `json:"implementationVersion"`
	ConnectionVersion     string `json:"connectionVersion"`
}
type Value struct {
	Type          string `json:"type"`
	Text          string `json:"text,omitempty"`
	SecretRef     string `json:"secretRef,omitempty"`
	SecretVersion string `json:"secretVersion,omitempty"`
}

func (v Value) Valid() bool {
	if v.Type == "secretRef" {
		return v.Text == "" && ValidIdentifier(v.SecretRef) && ValidIdentifier(v.SecretVersion)
	}
	if v.SecretRef != "" || v.SecretVersion != "" || len(v.Text) > 4096 {
		return false
	}
	switch v.Type {
	case "string":
		return true
	case "boolean":
		return v.Text == "true" || v.Text == "false"
	case "integer":
		n, err := strconv.ParseInt(v.Text, 10, 64)
		return err == nil && strconv.FormatInt(n, 10) == v.Text
	case "number":
		n, err := strconv.ParseFloat(v.Text, 64)
		return err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && strings.TrimSpace(v.Text) == v.Text
	default:
		return false
	}
}

type Constraints struct {
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Allowed []string `json:"allowed,omitempty"`
}
type ConfigItem struct {
	Key         string      `json:"key"`
	Version     string      `json:"version"`
	Value       Value       `json:"value"`
	Sensitive   bool        `json:"sensitive,omitempty"`
	Constraints Constraints `json:"constraints"`
}
type ConfigState struct {
	Target             Target            `json:"target"`
	Version            string            `json:"version"`
	SaveMode           string            `json:"saveMode"`
	ConditionalUpdates bool              `json:"conditionalUpdates"`
	Items              []ConfigItem      `json:"items"`
	SecretReferences   []SecretReference `json:"secretReferences,omitempty"`
}
type SecretReference struct {
	Ref      string   `json:"ref"`
	Versions []string `json:"versions"`
}
type Variant struct {
	Name   string `json:"name"`
	Weight int    `json:"weight"`
}
type ExperimentSpec struct {
	Variants       []Variant `json:"variants"`
	Audience       string    `json:"audience"`
	Metrics        []string  `json:"metrics"`
	ExclusionGroup string    `json:"exclusionGroup,omitempty"`
	Allocation     string    `json:"allocation"`
}

func (v ExperimentSpec) Valid() bool {
	if len(v.Variants) < 2 || len(v.Variants) > 10 || !ValidIdentifier(v.Audience) || !ValidIdentifier(v.Allocation) || len(v.Metrics) == 0 || len(v.Metrics) > 20 || (v.ExclusionGroup != "" && !ValidIdentifier(v.ExclusionGroup)) {
		return false
	}
	total := 0
	names := map[string]bool{}
	for _, item := range v.Variants {
		if !ValidIdentifier(item.Name) || names[item.Name] || item.Weight < 0 || item.Weight > 100 {
			return false
		}
		names[item.Name] = true
		total += item.Weight
	}
	if total != 100 {
		return false
	}
	names = map[string]bool{}
	for _, item := range v.Metrics {
		if !ValidIdentifier(item) || names[item] {
			return false
		}
		names[item] = true
	}
	return true
}

type ExperimentItem struct {
	Key     string         `json:"key"`
	Version string         `json:"version"`
	State   string         `json:"state"`
	Spec    ExperimentSpec `json:"spec"`
}
type ExperimentState struct {
	Target             Target           `json:"target"`
	Version            string           `json:"version"`
	DraftNoTraffic     bool             `json:"draftNoTraffic"`
	ConditionalUpdates bool             `json:"conditionalUpdates"`
	SupportsExclusion  bool             `json:"supportsExclusion"`
	AllocationModes    []string         `json:"allocationModes"`
	Items              []ExperimentItem `json:"items"`
}
type ConfigReader interface {
	Describe() Descriptor
	ReadConfig(context.Context, Target) (ConfigState, error)
}
type ExperimentReader interface {
	Describe() Descriptor
	ReadExperiments(context.Context, Target) (ExperimentState, error)
}

type Evidence struct {
	Source    string `json:"source"`
	ServiceID string `json:"serviceId"`
	HeadSHA   string `json:"headSha"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
type Candidate struct {
	Reason   string     `json:"reason,omitempty"`
	Origin   string     `json:"origin,omitempty"`
	Kind     string     `json:"kind"`
	Key      string     `json:"key"`
	Evidence []Evidence `json:"evidence"`
}

// AnalysisInput is assembled by the platform; no credentials or desired values.
type AnalysisInput struct {
	ServiceID    string            `json:"serviceId"`
	SnapshotHash string            `json:"snapshotHash"`
	BaseSHA      string            `json:"baseSha"`
	HeadSHA      string            `json:"headSha"`
	Patch        string            `json:"patch"`
	Goal         string            `json:"goal"`
	Environment  string            `json:"environment"`
	Resources    []ResourceContext `json:"resources"`
	WorktreePath string            `json:"-"`
}
type Analysis struct {
	Trace         *AnalysisTrace     `json:"trace,omitempty"`
	Questions     []AnalysisQuestion `json:"questions,omitempty"`
	SchemaVersion string             `json:"schemaVersion"`
	SnapshotHash  string             `json:"snapshotHash"`
	ServiceID     string             `json:"serviceId"`
	HeadSHA       string             `json:"headSha"`
	Candidates    []Candidate        `json:"candidates"`
}
type Analyzer interface {
	Version() string
	Analyze(context.Context, AnalysisInput) (Analysis, error)
}

type ConfigRequirement struct {
	Key     string `json:"key"`
	Desired *Value `json:"desired,omitempty"`
	Reason  string `json:"reason"`
}
type ExperimentRequirement struct {
	Key     string          `json:"key"`
	Desired *ExperimentSpec `json:"desired,omitempty"`
	Reason  string          `json:"reason"`
}
type Requirement struct {
	ServiceID             string                  `json:"serviceId"`
	Config                []ConfigRequirement     `json:"config,omitempty"`
	Experiments           []ExperimentRequirement `json:"experiments,omitempty"`
	ConfigNotInvolved     string                  `json:"configNotInvolved,omitempty"`
	ExperimentNotInvolved string                  `json:"experimentNotInvolved,omitempty"`
}

// Normalize validates all user fields before any external read. Source text and
// explicit requirements are evidence, never connection or authorization input.
func Normalize(requirements []Requirement, serviceIDs []string) ([]Requirement, error) {
	if len(requirements) > len(serviceIDs) {
		return nil, ErrInvalid
	}
	result := Clone(requirements)
	seen := map[string]bool{}
	for i := range result {
		r := &result[i]
		if !slices.Contains(serviceIDs, r.ServiceID) || seen[r.ServiceID] || len(r.Config) > 50 || len(r.Experiments) > 20 || len(r.ConfigNotInvolved) > 1024 || len(r.ExperimentNotInvolved) > 1024 {
			return nil, ErrInvalid
		}
		seen[r.ServiceID] = true
		keys := map[string]bool{}
		for _, c := range r.Config {
			if !ValidIdentifier(c.Key) || keys[c.Key] || strings.TrimSpace(c.Reason) == "" || len(c.Reason) > 1024 || (c.Desired != nil && !c.Desired.Valid()) {
				return nil, ErrInvalid
			}
			keys[c.Key] = true
		}
		keys = map[string]bool{}
		for _, c := range r.Experiments {
			if !ValidIdentifier(c.Key) || keys[c.Key] || strings.TrimSpace(c.Reason) == "" || len(c.Reason) > 1024 || (c.Desired != nil && !c.Desired.Valid()) {
				return nil, ErrInvalid
			}
			keys[c.Key] = true
		}
		slices.SortFunc(r.Config, func(a, b ConfigRequirement) int { return strings.Compare(a.Key, b.Key) })
		slices.SortFunc(r.Experiments, func(a, b ExperimentRequirement) int { return strings.Compare(a.Key, b.Key) })
	}
	slices.SortFunc(result, func(a, b Requirement) int { return strings.Compare(a.ServiceID, b.ServiceID) })
	return result, nil
}

type ReviewEvidence struct {
	TaskID      string `json:"taskId,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	ArtifactID  string `json:"artifactId,omitempty"`
	State       string `json:"state"`
}
type Source struct {
	Analysis           *AnalysisTrace     `json:"analysis,omitempty"`
	Questions          []AnalysisQuestion `json:"questions,omitempty"`
	ServiceID          string             `json:"serviceId"`
	CatalogVersion     string             `json:"catalogVersion"`
	HeadSHA            string             `json:"headSha"`
	BaseSHA            string             `json:"baseSha"`
	Review             ReviewEvidence     `json:"review"`
	ConfigCoverage     string             `json:"configCoverage"`
	ExperimentCoverage string             `json:"experimentCoverage"`
	ConfigReason       string             `json:"configReason,omitempty"`
	ExperimentReason   string             `json:"experimentReason,omitempty"`
	Candidates         []Candidate        `json:"candidates"`
}
type Issue struct {
	Code      string `json:"code"`
	ServiceID string `json:"serviceId,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Key       string `json:"key,omitempty"`
}
type Observation struct {
	Kind       string     `json:"kind"`
	Target     Target     `json:"target"`
	Descriptor Descriptor `json:"descriptor"`
	Version    string     `json:"version"`
	ObservedAt time.Time  `json:"observedAt"`
}
type Proposal struct {
	ServiceID  string          `json:"serviceId"`
	Value      *Value          `json:"value,omitempty"`
	Experiment *ExperimentSpec `json:"experiment,omitempty"`
	Evidence   []Evidence      `json:"evidence"`
}
type Action struct {
	ID                string          `json:"id"`
	Hash              string          `json:"hash"`
	Kind              string          `json:"kind"`
	Key               string          `json:"key"`
	Operation         string          `json:"operation"`
	Target            Target          `json:"target"`
	Descriptor        Descriptor      `json:"descriptor"`
	ResourceVersion   string          `json:"resourceVersion"`
	ExpectedVersion   string          `json:"expectedVersion,omitempty"`
	MustNotExist      bool            `json:"mustNotExist"`
	Effect            string          `json:"effect"`
	CurrentValue      *Value          `json:"currentValue,omitempty"`
	CurrentExperiment *ExperimentSpec `json:"currentExperiment,omitempty"`
	Proposals         []Proposal      `json:"proposals"`
}
type Plan struct {
	ID              string        `json:"id"`
	Revision        uint64        `json:"revision"`
	SchemaVersion   string        `json:"schemaVersion"`
	TenantID        string        `json:"tenantId"`
	Environment     string        `json:"environment"`
	SnapshotID      string        `json:"snapshotId"`
	SnapshotHash    string        `json:"snapshotHash"`
	CreatedBy       string        `json:"createdBy"`
	State           string        `json:"state"`
	Hash            string        `json:"hash"`
	AnalyzerVersion string        `json:"analyzerVersion"`
	Services        []Source      `json:"services"`
	Actions         []Action      `json:"actions"`
	Issues          []Issue       `json:"issues"`
	Observations    []Observation `json:"observations"`
	CreatedAt       time.Time     `json:"createdAt"`
}
type ServiceInput struct {
	WorktreePath  string
	Source        Source
	Bindings      EnvironmentBinding
	Patch         string
	AnalysisError string
	Requirement   Requirement
}
type Input struct {
	Goal                                                       string
	TenantID, Environment, SnapshotID, SnapshotHash, CreatedBy string
	Services                                                   []ServiceInput
}

func Hash(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func Clone[T any](value T) T {
	data, _ := json.Marshal(value)
	var result T
	_ = json.Unmarshal(data, &result)
	return result
}
