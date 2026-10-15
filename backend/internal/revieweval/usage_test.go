package revieweval_test

import (
	"testing"

	"agent-platform/backend/internal/review"
	"agent-platform/backend/internal/revieweval"
)

func TestUsageAndCostAreCompleteEstimatesWithExplicitPrices(t *testing.T) {
	capture := sampleCapture()
	for i := range capture.Results {
		cached := int64(40)
		capture.Results[i].Usage = &review.ModelUsage{Requests: 1, MeasuredRequests: 1, InputTokens: 100, OutputTokens: 20, TotalTokens: 120, CachedInputTokens: &cached, Complete: true}
	}
	prices := revieweval.Pricing{SchemaVersion: "1.0", Model: "fixture", GatewayURL: "https://model.example.invalid", Currency: "USD", SourceURL: "https://model.example.invalid/pricing", AsOf: "2026-10-15", RatePeriod: "test", InputPerMillion: 1, CachedInputPerMillion: .1, OutputPerMillion: 2}
	capture.Runtime.GatewayURL = prices.GatewayURL
	labels := &revieweval.Adjudications{SchemaVersion: "1.0", CaptureSHA256: revieweval.CaptureSHA256(capture), Reviewer: "assistant-test", ReviewerKind: "assistant"}
	report, err := revieweval.ScoreWithPricing(capture, labels, &prices)
	if err != nil {
		t.Fatal(err)
	}
	if report.TokenUsage == nil || *report.TokenUsage != 720 || report.Usage == nil || report.Usage.Requests != 6 || report.CostUSD != nil || report.EstimatedCostUSD == nil || *report.EstimatedCostUSD < .0006239 || *report.EstimatedCostUSD > .0006241 || report.AdjudicationKind != "assistant" {
		t.Fatalf("wrong measurement/estimate: %+v", report)
	}
	for _, scenario := range []string{"old-capture", "missing-cache", "partial", "impossible-usage", "wrong-model", "negative-price"} {
		t.Run(scenario, func(t *testing.T) {
			changed := capture
			changed.Results = append([]revieweval.Result(nil), capture.Results...)
			u := *capture.Results[0].Usage
			changed.Results[0].Usage = &u
			p := prices
			switch scenario {
			case "old-capture":
				changed.Results[0].Usage = nil
			case "missing-cache":
				u.CachedInputTokens = nil
			case "partial":
				u.Complete = false
			case "impossible-usage":
				u.TotalTokens = 1
			case "wrong-model":
				p.Model = "wrong"
			case "negative-price":
				p.OutputPerMillion = -1
			}
			r, err := revieweval.ScoreWithPricing(changed, nil, &p)
			if scenario == "impossible-usage" || scenario == "wrong-model" || scenario == "negative-price" {
				if err == nil {
					t.Fatal("accepted invalid usage/pricing")
				}
				return
			}
			if err != nil || r.EstimatedCostUSD != nil {
				t.Fatal("unknown usage became a cost estimate", err)
			}
			if scenario != "missing-cache" && r.TokenUsage != nil {
				t.Fatal("partial tokens presented as a complete total")
			}
		})
	}
}
