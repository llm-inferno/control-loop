package collector

import "testing"

func TestParseLatest(t *testing.T) {
	body := []byte(`{
		"effectiveInput": {"RPS": 5, "maxConcurrency": 32, "avgInputTokens": 1024, "avgOutputTokens": 512, "accelerator":"H100","model":"m"},
		"result": {"throughput": 4.5, "avgTTFT": 120, "avgITL": 11, "maxRPS": 6, "saturation": ""},
		"completedAt": "2026-06-18T10:00:00Z"
	}`)
	env, err := parseLatest(body)
	if err != nil {
		t.Fatalf("parseLatest: %v", err)
	}
	if env.EffectiveInput.MaxConcurrency != 32 || env.EffectiveInput.RPS != 5 {
		t.Fatalf("effectiveInput wrong: %+v", env.EffectiveInput)
	}
	if env.Result.AvgITL != 11 || env.Result.Throughput != 4.5 {
		t.Fatalf("result wrong: %+v", env.Result)
	}
	if env.CompletedAt == "" {
		t.Fatal("completedAt empty")
	}
}
