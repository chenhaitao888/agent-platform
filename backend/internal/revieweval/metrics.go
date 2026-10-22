package revieweval

import (
	"math"
	"slices"
)

func addMetrics(dst *Metrics, src Metrics) {
	dst.Cases += src.Cases
	if dst.States == nil {
		dst.States = map[string]int{}
	}
	for state, count := range src.States {
		dst.States[state] += count
	}
	dst.ExpectedIssues += src.ExpectedIssues
	dst.SeriousIssues += src.SeriousIssues
	dst.TruePositives += src.TruePositives
	dst.FalsePositives += src.FalsePositives
	dst.Advisories += src.Advisories
	dst.FalseNegatives += src.FalseNegatives
	dst.SeriousFound += src.SeriousFound
	dst.PendingFindings += src.PendingFindings
	dst.TotalDurationMS += src.TotalDurationMS
}

func finalize(m *Metrics, durations []int64) {
	m.DurationSamples = len(durations)
	if m.PendingFindings == 0 {
		// Precision measures planted regression findings among ALL adjudicated
		// findings. Separating useful advisories must never improve this ratio.
		m.Precision = rate(m.TruePositives, m.TruePositives+m.FalsePositives+m.Advisories)
		m.Recall = rate(m.TruePositives, m.ExpectedIssues)
		m.SeriousRecall = rate(m.SeriousFound, m.SeriousIssues)
	}
	if len(durations) != 0 {
		slices.Sort(durations)
		m.P50DurationMS = durations[int(math.Ceil(.5*float64(len(durations))))-1]
		m.P95DurationMS = durations[int(math.Ceil(.95*float64(len(durations))))-1]
	}
}
