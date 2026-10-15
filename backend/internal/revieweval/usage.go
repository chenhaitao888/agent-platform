package revieweval

import (
	"bytes"
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"time"

	"agent-platform/backend/internal/review"
)

// Pricing is an explicit offline rate snapshot, never a live billing claim.
// RatePeriod names the caller's selected tariff assumption for the whole run.
type Pricing struct {
	SchemaVersion         string  `json:"schemaVersion"`
	Model                 string  `json:"model"`
	GatewayURL            string  `json:"gatewayUrl"`
	Currency              string  `json:"currency"`
	SourceURL             string  `json:"sourceUrl"`
	AsOf                  string  `json:"asOf"`
	RatePeriod            string  `json:"ratePeriod"`
	InputPerMillion       float64 `json:"inputPerMillion"`
	CachedInputPerMillion float64 `json:"cachedInputPerMillion"`
	OutputPerMillion      float64 `json:"outputPerMillion"`
}

func (p *Pricing) UnmarshalJSON(data []byte) error {
	// An omitted/null price must not silently become a zero-dollar tariff.
	var required struct {
		Input  *float64 `json:"inputPerMillion"`
		Cached *float64 `json:"cachedInputPerMillion"`
		Output *float64 `json:"outputPerMillion"`
	}
	if json.Unmarshal(data, &required) != nil || required.Input == nil || required.Cached == nil || required.Output == nil {
		return ErrInvalidEvaluation
	}
	type wire Pricing
	var value wire
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil {
		return ErrInvalidEvaluation
	}
	*p = Pricing(value)
	return nil
}

func scoreUsage(report *Report, capture Capture, pricing *Pricing) error {
	zero := int64(0)
	total := review.ModelUsage{Complete: true, CachedInputTokens: &zero}
	observed := false
	for _, result := range capture.Results {
		u := result.Usage
		if u == nil {
			noInference := result.State == "skipped" || result.State == "preparation_failed" || ((result.State == "timeout" || result.State == "canceled") && result.PatchSHA256 == "")
			if !noInference {
				total.Complete = false
				total.CachedInputTokens = nil
			}
			continue
		}
		if !validUsage(u) {
			return ErrInvalidEvaluation
		}
		observed = true
		total.Requests += u.Requests
		total.MeasuredRequests += u.MeasuredRequests
		total.InputTokens += u.InputTokens
		total.OutputTokens += u.OutputTokens
		total.TotalTokens += u.TotalTokens
		total.Complete = total.Complete && u.Complete
		if u.CachedInputTokens == nil {
			total.CachedInputTokens = nil
		} else if total.CachedInputTokens != nil {
			*total.CachedInputTokens += *u.CachedInputTokens
		}
	}
	if observed {
		report.Usage = &total
		if total.Complete {
			tokens := total.TotalTokens
			report.TokenUsage = &tokens
		}
	}
	if pricing != nil {
		if !validPricing(*pricing, capture.Runtime) {
			return ErrInvalidEvaluation
		}
		copy := *pricing
		report.Pricing = &copy
		if observed && total.Complete && total.CachedInputTokens != nil {
			cached := *total.CachedInputTokens
			cost := (float64(total.InputTokens-cached)*pricing.InputPerMillion + float64(cached)*pricing.CachedInputPerMillion + float64(total.OutputTokens)*pricing.OutputPerMillion) / 1_000_000
			report.EstimatedCostUSD = &cost
		}
	}
	return nil
}

func validUsage(u *review.ModelUsage) bool {
	const maxTokens = int64(64_000_000_000_000)
	if u.Requests < 0 || u.Requests > 64 || u.MeasuredRequests < 0 || u.MeasuredRequests > u.Requests || (u.Complete && u.Requests != u.MeasuredRequests) || u.InputTokens < 0 || u.OutputTokens < 0 || u.InputTokens > maxTokens || u.OutputTokens > maxTokens || u.TotalTokens != u.InputTokens+u.OutputTokens {
		return false
	}
	if u.MeasuredRequests == 0 && u.TotalTokens != 0 {
		return false
	}
	return u.CachedInputTokens == nil || (*u.CachedInputTokens >= 0 && *u.CachedInputTokens <= u.InputTokens)
}

func validPricing(p Pricing, runtime review.RuntimeIdentity) bool {
	if p.SchemaVersion != "1.0" || p.Model != runtime.Model || p.GatewayURL != runtime.GatewayURL || p.Currency != "USD" || strings.TrimSpace(p.RatePeriod) == "" || len(p.RatePeriod) > 128 {
		return false
	}
	if _, err := time.Parse("2006-01-02", p.AsOf); err != nil {
		return false
	}
	for _, raw := range []string{p.SourceURL, p.GatewayURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	for _, value := range []float64{p.InputPerMillion, p.CachedInputPerMillion, p.OutputPerMillion} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e9 {
			return false
		}
	}
	return true
}
