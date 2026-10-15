package review

import "context"

// ModelUsage contains only numeric counters observed by the host transport.
// Incomplete counters are a lower bound, never a bill or an assumed zero cost.
type ModelUsage struct {
	Requests          int    `json:"requests"`
	MeasuredRequests  int    `json:"measuredRequests"`
	InputTokens       int64  `json:"inputTokens"`
	OutputTokens      int64  `json:"outputTokens"`
	TotalTokens       int64  `json:"totalTokens"`
	CachedInputTokens *int64 `json:"cachedInputTokens"`
	Complete          bool   `json:"complete"`
}

// MeteredRunner returns usage for this invocation, including failed runs. It
// does not expose mutable "last run" state shared by concurrent executions.
type MeteredRunner interface {
	RunWithUsage(context.Context, RunInput) (FindingsReport, *ModelUsage, error)
}
