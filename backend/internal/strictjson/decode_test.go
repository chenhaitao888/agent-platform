package strictjson_test

import (
	"agent-platform/backend/internal/strictjson"
	"testing"
	"time"
)

func TestTimestampScalarKeepsStrictObjectValidation(t *testing.T) {
	for _, body := range []string{`{"at":null}`, `{"at":123}`, `{"at":{}}`, `{"at":"tomorrow"}`, `{"at":"2026-99-17T00:00:00Z"}`, `{"at":"2026-11-17T00:00:00Z","at":"2026-11-17T00:00:00Z"}`, `{"At":"2026-11-17T00:00:00Z"}`, `{"at":"2026-11-17T00:00:00Z","override":true}`} {
		var value struct {
			At time.Time `json:"at"`
		}
		if strictjson.Decode([]byte(body), &value) == nil {
			t.Fatalf("accepted invalid timestamp/object: %s", body)
		}
	}
	var value struct {
		At time.Time `json:"at"`
	}
	if strictjson.Decode([]byte(`{"at":"2026-11-17T00:00:00.123456Z"}`), &value) != nil || value.At.Nanosecond() != 123456000 {
		t.Fatal("valid timestamp rejected")
	}
}
