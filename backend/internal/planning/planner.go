package planning

import (
	"context"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Planner struct {
	configs     map[string]ConfigReader
	experiments map[string]ExperimentReader
	analyzer    Analyzer
}

func NewPlanner(configs map[string]ConfigReader, experiments map[string]ExperimentReader, analyzer Analyzer) (*Planner, error) {
	if analyzer == nil || !ValidIdentifier(analyzer.Version()) {
		return nil, ErrInvalid
	}
	p := &Planner{configs: map[string]ConfigReader{}, experiments: map[string]ExperimentReader{}, analyzer: analyzer}
	for id, r := range configs {
		if !ValidIdentifier(id) || r == nil || !validDescriptor(r.Describe(), "config") {
			return nil, ErrInvalid
		}
		p.configs[id] = r
	}
	for id, r := range experiments {
		if !ValidIdentifier(id) || r == nil || !validDescriptor(r.Describe(), "experiment") {
			return nil, ErrInvalid
		}
		p.experiments[id] = r
	}
	return p, nil
}
func validDescriptor(d Descriptor, kind string) bool {
	return d.Kind == kind && d.ContractVersion == kind+"-read.v1" && ValidIdentifier(d.ImplementationVersion) && ValidIdentifier(d.ConnectionVersion)
}
func (p *Planner) Registered(kind, id string) bool {
	if p == nil {
		return false
	}
	if kind == "config" {
		return p.configs[id] != nil
	}
	return kind == "experiment" && p.experiments[id] != nil
}

type resourceKey struct {
	kind   string
	target Target
}
type observed struct {
	descriptor  Descriptor
	config      ConfigState
	experiments ExperimentState
	err         error
}

func (p *Planner) Build(ctx context.Context, in Input) Plan {
	plan := Plan{SchemaVersion: "change-plan.v1", TenantID: in.TenantID, Environment: in.Environment, SnapshotID: in.SnapshotID, SnapshotHash: in.SnapshotHash, CreatedBy: in.CreatedBy, AnalyzerVersion: p.analyzer.Version(), State: "READY_FOR_REVIEW", Services: []Source{}, Actions: []Action{}, Issues: []Issue{}, Observations: []Observation{}, CreatedAt: time.Now().UTC()}
	cache := map[resourceKey]observed{}
	for _, service := range in.Services {
		source := service.Source
		source.Candidates = []Candidate{}
		if source.Review.State != "SUCCEEDED" {
			addIssue(&plan, "review_incomplete", source.ServiceID, "", "")
		}
		if service.AnalysisError != "" || ctx.Err() != nil {
			source.ConfigCoverage, source.ExperimentCoverage = "NOT_ANALYZED", "NOT_ANALYZED"
			code := service.AnalysisError
			if code == "" {
				code = "analysis_canceled"
			}
			addIssue(&plan, code, source.ServiceID, "", "")
			plan.Services = append(plan.Services, source)
			continue
		}
		analysis, err := p.analyzer.Analyze(ctx, AnalysisInput{ServiceID: source.ServiceID, SnapshotHash: in.SnapshotHash, BaseSHA: source.BaseSHA, HeadSHA: source.HeadSHA, Patch: service.Patch})
		if err != nil || !validAnalysis(analysis, in.SnapshotHash, source) {
			source.ConfigCoverage, source.ExperimentCoverage = "ANALYSIS_FAILED", "ANALYSIS_FAILED"
			addIssue(&plan, "analysis_failed", source.ServiceID, "", "")
			plan.Services = append(plan.Services, source)
			continue
		}
		source.Candidates = Clone(analysis.Candidates)
		proposals := map[string]Proposal{}
		for _, c := range source.Candidates {
			proposals[c.Kind+":"+c.Key] = Proposal{ServiceID: source.ServiceID, Evidence: c.Evidence}
		}
		merge := func(kind, key, reason string, value *Value, experiment *ExperimentSpec) {
			k := kind + ":" + key
			proposal, exists := proposals[k]
			if !exists {
				proposal = Proposal{ServiceID: source.ServiceID, Evidence: []Evidence{}}
				source.Candidates = append(source.Candidates, Candidate{Kind: kind, Key: key, Evidence: []Evidence{}})
			}
			evidence := Evidence{Source: "user-requirement", ServiceID: source.ServiceID, HeadSHA: source.HeadSHA, Reason: reason}
			proposal.Evidence = append(proposal.Evidence, evidence)
			proposal.Value, proposal.Experiment = value, experiment
			proposals[k] = proposal
			for i := range source.Candidates {
				if source.Candidates[i].Kind == kind && source.Candidates[i].Key == key {
					source.Candidates[i].Evidence = append(source.Candidates[i].Evidence, evidence)
				}
			}
		}
		for _, r := range service.Requirement.Config {
			merge("config", r.Key, r.Reason, r.Desired, nil)
		}
		for _, r := range service.Requirement.Experiments {
			merge("experiment", r.Key, r.Reason, nil, r.Desired)
		}
		slices.SortFunc(source.Candidates, func(a, b Candidate) int { return strings.Compare(a.Kind+":"+a.Key, b.Kind+":"+b.Key) })
		for _, kind := range []string{"config", "experiment"} {
			candidates := []Candidate{}
			for _, c := range source.Candidates {
				if c.Kind == kind {
					candidates = append(candidates, c)
				}
			}
			reason := service.Requirement.ConfigNotInvolved
			binding := service.Bindings.Config
			if kind == "experiment" {
				reason = service.Requirement.ExperimentNotInvolved
				binding = service.Bindings.Experiment
			}
			coverage := "CANDIDATES_FOUND"
			if len(candidates) == 0 {
				coverage = "NO_CANDIDATES"
				if strings.TrimSpace(reason) != "" {
					coverage = "NOT_INVOLVED"
				} else {
					addIssue(&plan, "coverage_confirmation_required", source.ServiceID, kind, "")
				}
			} else if strings.TrimSpace(reason) != "" {
				addIssue(&plan, "coverage_conflict", source.ServiceID, kind, "")
			}
			if kind == "config" {
				source.ConfigCoverage, source.ConfigReason = coverage, reason
			} else {
				source.ExperimentCoverage, source.ExperimentReason = coverage, reason
			}
			if len(candidates) == 0 {
				continue
			}
			if binding == nil || !p.Registered(kind, binding.ConnectorRef) {
				for _, c := range candidates {
					addIssue(&plan, "binding_unavailable", source.ServiceID, kind, c.Key)
				}
				continue
			}
			target := Target{TenantID: in.TenantID, Environment: in.Environment, ConnectorRef: binding.ConnectorRef, ResourceRef: binding.ResourceRef}
			key := resourceKey{kind: kind, target: target}
			state, exists := cache[key]
			if !exists {
				state = p.observe(ctx, kind, target)
				cache[key] = state
				if state.err == nil {
					version := state.config.Version
					if kind == "experiment" {
						version = state.experiments.Version
					}
					plan.Observations = append(plan.Observations, Observation{Kind: kind, Target: target, Descriptor: state.descriptor, Version: version, ObservedAt: time.Now().UTC()})
				}
			}
			for _, c := range candidates {
				if state.err != nil {
					addIssue(&plan, "platform_read_failed", source.ServiceID, kind, c.Key)
					continue
				}
				proposal := proposals[kind+":"+c.Key]
				action := Action{Kind: kind, Key: c.Key, Target: target, Descriptor: state.descriptor, Proposals: []Proposal{proposal}}
				if kind == "config" {
					configAction(&plan, &action, state.config)
				} else {
					experimentAction(&plan, &action, state.experiments)
				}
				mergeAction(&plan, action)
			}
		}
		plan.Services = append(plan.Services, source)
	}
	if len(plan.Issues) > 0 {
		plan.State = "BLOCKED"
	}
	for i := range plan.Actions {
		a := &plan.Actions[i]
		a.ID = "action-" + strconv.Itoa(i+1)
		a.Hash = Hash(*a)
	}
	return plan
}
func addIssue(p *Plan, code, service, kind, key string) {
	p.Issues = append(p.Issues, Issue{Code: code, ServiceID: service, Kind: kind, Key: key})
}
func validAnalysis(a Analysis, snapshot string, source Source) bool {
	if a.SchemaVersion != "change-candidates.v1" || a.SnapshotHash != snapshot || a.ServiceID != source.ServiceID || a.HeadSHA != source.HeadSHA || a.Candidates == nil || len(a.Candidates) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range a.Candidates {
		key := c.Kind + ":" + c.Key
		if (c.Kind != "config" && c.Kind != "experiment") || !ValidIdentifier(c.Key) || seen[key] || len(c.Evidence) == 0 || len(c.Evidence) > 10 {
			return false
		}
		seen[key] = true
		for _, e := range c.Evidence {
			if e.Source != "fixed-diff" || e.ServiceID != source.ServiceID || e.HeadSHA != source.HeadSHA || e.Line <= 0 || e.Line > 2147483647 || len(e.Path) > 256 || e.Path == "" || path.Clean(e.Path) != e.Path || strings.HasPrefix(e.Path, "/") || strings.HasPrefix(e.Path, "..") || strings.ContainsAny(e.Path, "\\\x00\t\n") || e.Reason != "" {
				return false
			}
		}
	}
	return true
}
func (p *Planner) observe(ctx context.Context, kind string, target Target) observed {
	result := observed{}
	if ctx.Err() != nil {
		result.err = ctx.Err()
		return result
	}
	if kind == "config" {
		r := p.configs[target.ConnectorRef]
		result.descriptor = r.Describe()
		result.config, result.err = r.ReadConfig(ctx, target)
		if result.err == nil && !ValidConfigState(result.config, target) {
			result.err = ErrInvalid
		}
	} else {
		r := p.experiments[target.ConnectorRef]
		result.descriptor = r.Describe()
		result.experiments, result.err = r.ReadExperiments(ctx, target)
		if result.err == nil && !ValidExperimentState(result.experiments, target) {
			result.err = ErrInvalid
		}
	}
	if !validDescriptor(result.descriptor, kind) {
		result.err = ErrInvalid
	}
	return result
}
func ValidConfigState(s ConfigState, target Target) bool {
	if s.Target != target || !ValidIdentifier(s.Version) || s.Items == nil || len(s.Items) > 1000 || len(s.SecretReferences) > 100 || !slices.Contains([]string{"draft", "publish_required", "immediate", "unknown"}, s.SaveMode) {
		return false
	}
	seen := map[string]bool{}
	for _, ref := range s.SecretReferences {
		if !ValidIdentifier(ref.Ref) || seen[ref.Ref] || len(ref.Versions) == 0 || len(ref.Versions) > 100 {
			return false
		}
		seen[ref.Ref] = true
		for _, version := range ref.Versions {
			if !ValidIdentifier(version) {
				return false
			}
		}
	}
	seen = map[string]bool{}
	for _, item := range s.Items {
		if !ValidIdentifier(item.Key) || !ValidIdentifier(item.Version) || seen[item.Key] || !item.Value.Valid() || (item.Sensitive && item.Value.Type != "secretRef") || len(item.Constraints.Allowed) > 100 {
			return false
		}
		seen[item.Key] = true
		for _, bound := range []*float64{item.Constraints.Min, item.Constraints.Max} {
			if bound != nil && ((item.Value.Type != "integer" && item.Value.Type != "number") || math.IsNaN(*bound) || math.IsInf(*bound, 0) || (item.Value.Type == "integer" && (math.Abs(*bound) > 9007199254740991 || math.Trunc(*bound) != *bound))) {
				return false
			}
		}
		if item.Constraints.Min != nil && item.Constraints.Max != nil && *item.Constraints.Min > *item.Constraints.Max {
			return false
		}
		for _, v := range item.Constraints.Allowed {
			if len(v) > 4096 {
				return false
			}
		}
	}
	return true
}
func ValidExperimentState(s ExperimentState, target Target) bool {
	if s.Target != target || !ValidIdentifier(s.Version) || s.Items == nil || len(s.Items) > 1000 || len(s.AllocationModes) > 10 {
		return false
	}
	for _, mode := range s.AllocationModes {
		if !ValidIdentifier(mode) {
			return false
		}
	}
	seen := map[string]bool{}
	for _, item := range s.Items {
		if !ValidIdentifier(item.Key) || !ValidIdentifier(item.Version) || seen[item.Key] || !slices.Contains([]string{"DRAFT", "ACTIVE", "ARCHIVED"}, item.State) || !item.Spec.Valid() {
			return false
		}
		seen[item.Key] = true
	}
	return true
}
func configAction(plan *Plan, a *Action, s ConfigState) {
	p := &a.Proposals[0]
	a.Operation = "config.create"
	a.MustNotExist = true
	a.ResourceVersion = s.Version
	a.Effect = s.SaveMode
	var existing *ConfigItem
	for _, item := range s.Items {
		if item.Key == a.Key {
			copy := item
			existing = &copy
			break
		}
	}
	if existing != nil {
		a.Operation = "config.update"
		a.MustNotExist = false
		a.ExpectedVersion = existing.Version
		v := existing.Value
		a.CurrentValue = &v
	}
	issue := func(code string) { addIssue(plan, code, p.ServiceID, "config", a.Key) }
	if s.SaveMode == "unknown" {
		issue("config_effect_unknown")
	}
	if existing != nil && !s.ConditionalUpdates {
		issue("conditional_update_unsupported")
	}
	if p.Value == nil {
		issue("desired_value_required")
		return
	}
	if existing != nil {
		if (existing.Sensitive || existing.Value.Type == "secretRef") && p.Value.Type != "secretRef" {
			p.Value = nil
			issue("secret_reference_required")
			return
		}
		if existing.Value.Type != p.Value.Type {
			issue("config_type_mismatch")
			return
		}
		c := existing.Constraints
		if len(c.Allowed) > 0 && !slices.Contains(c.Allowed, p.Value.Text) {
			issue("config_value_out_of_range")
		}
		if p.Value.Type == "integer" || p.Value.Type == "number" {
			n, _ := strconv.ParseFloat(p.Value.Text, 64)
			if (c.Min != nil && n < *c.Min) || (c.Max != nil && n > *c.Max) {
				issue("config_value_out_of_range")
			}
		}
		if existing.Value == *p.Value {
			a.Operation = "config.no_change"
		}
	}
	if p.Value.Type == "secretRef" {
		registered := false
		for _, ref := range s.SecretReferences {
			if ref.Ref == p.Value.SecretRef && slices.Contains(ref.Versions, p.Value.SecretVersion) {
				registered = true
			}
		}
		if !registered {
			issue("secret_reference_unregistered")
		}
	}
}
func experimentAction(plan *Plan, a *Action, s ExperimentState) {
	p := &a.Proposals[0]
	a.Operation = "experiment.create_draft"
	a.MustNotExist = true
	a.ResourceVersion = s.Version
	a.Effect = "inactive_draft"
	issue := func(code string) { addIssue(plan, code, p.ServiceID, "experiment", a.Key) }
	if !s.DraftNoTraffic {
		issue("inactive_draft_unsupported")
	}
	for _, item := range s.Items {
		if item.Key == a.Key {
			a.Operation = "experiment.update_draft"
			a.MustNotExist = false
			a.ExpectedVersion = item.Version
			spec := Clone(item.Spec)
			a.CurrentExperiment = &spec
			if item.State != "DRAFT" {
				issue("experiment_not_draft")
			}
			if !s.ConditionalUpdates {
				issue("conditional_update_unsupported")
			}
			break
		}
	}
	if p.Experiment == nil {
		issue("experiment_fields_required")
		return
	}
	if !slices.Contains(s.AllocationModes, p.Experiment.Allocation) {
		issue("allocation_unsupported")
	}
	if p.Experiment.ExclusionGroup != "" && !s.SupportsExclusion {
		issue("exclusion_unsupported")
	}
}
func mergeAction(plan *Plan, next Action) {
	for i := range plan.Actions {
		a := &plan.Actions[i]
		if a.Kind == next.Kind && a.Target == next.Target && a.Key == next.Key {
			prior, incoming := a.Proposals[0], next.Proposals[0]
			if Hash(prior.Value) != Hash(incoming.Value) || Hash(prior.Experiment) != Hash(incoming.Experiment) {
				addIssue(plan, "shared_resource_conflict", incoming.ServiceID, a.Kind, a.Key)
			}
			a.Proposals = append(a.Proposals, incoming)
			return
		}
	}
	plan.Actions = append(plan.Actions, next)
}
