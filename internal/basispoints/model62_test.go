package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGPT62RequestEfforts(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		for _, tc := range []struct{ requested, want string }{
			{"", "medium"}, {"low", "low"}, {"medium", "medium"}, {"high", "high"},
			{"xhigh", "xhigh"}, {"max", "max"}, {" MAX ", "max"},
			{"none", "low"}, {"minimal", "low"}, {"extra-high", "xhigh"},
			{"extra_high", "xhigh"}, {"x-high", "xhigh"},
		} {
			t.Run(model+"/"+tc.requested, func(t *testing.T) {
				raw, _ := json.Marshal(object{"model": model, "input": "fixture", "reasoning": object{"effort": tc.requested}})
				prepared, bridge, err := Prepare(raw, "fixture", nil)
				if err != nil {
					t.Fatal(err)
				}
				var body object
				if err := decode(prepared, &body); err != nil {
					t.Fatal(err)
				}
				if body["model"] != model || body["model_selection"] != "explicit" || body["reasoning_effort"] != tc.want || bridge.Effort != tc.want || bridge.RequestedEffort != tc.requested {
					t.Fatal("model or requested effort was lost in translation")
				}
			})
		}
		for _, effort := range []string{"ultra", "automatic", "bad"} {
			raw, _ := json.Marshal(object{"model": model, "input": "fixture", "reasoning": object{"effort": effort}})
			if _, _, err := Prepare(raw, "fixture", nil); err == nil {
				t.Fatal("unsupported GPT-6.2 effort silently accepted", effort)
			}
		}
	}
}

func TestGPT62HistoricalMaxAndCurrentPrecedence(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol", "gpt-6-astra", "gpt-6.1-sol"} {
		for _, current := range []string{"", "medium"} {
			t.Run(model+"/"+current, func(t *testing.T) {
				body := object{"model": model, "input": []any{
					object{"type": "configuration_update", "reasoning": object{"effort": "low"}},
					object{"type": "message", "role": "user", "content": "fixture"},
					object{"type": "configuration_update", "reasoning": object{"effort": "max"}},
				}}
				if current != "" {
					body["reasoning"] = object{"effort": current}
				}
				raw, _ := json.Marshal(body)
				prepared, bridge, err := Prepare(raw, "fixture", nil)
				if err != nil {
					t.Fatal(err)
				}
				want := "xhigh"
				if model == "gpt-6.2-astra" || model == "gpt-6.2-sol" {
					want = "max"
				}
				if current != "" {
					want = current
				}
				var out object
				if err := decode(prepared, &out); err != nil {
					t.Fatal(err)
				}
				if bridge.Effort != want || out["reasoning_effort"] != want || strings.Contains(string(prepared), "configuration_update") {
					t.Fatal("historical max was prematurely downgraded or current request was ignored")
				}
			})
		}
	}
}

func TestGPT62EffortInputPrecedence(t *testing.T) {
	for _, tc := range []struct {
		fields object
		want   string
	}{
		{object{"reasoning_effort": "max"}, "max"},
		{object{"reasoning_effort": "high", "reasoning": object{"effort": "max"}}, "max"},
		{object{"reasoning_effort": "max", "reasoning": object{"effort": "medium"}}, "medium"},
	} {
		body := object{"model": "gpt-6.2-sol", "input": "fixture"}
		for k, v := range tc.fields {
			body[k] = v
		}
		raw, _ := json.Marshal(body)
		_, b, err := Prepare(raw, "fixture", nil)
		if err != nil || b.Effort != tc.want {
			t.Fatal("effort precedence changed", err)
		}
	}
}

func TestLegacyAndUnknownModelEffortsUnchanged(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol", "unknown-model", "gpt-6.2-sol-preview"} {
		for _, effort := range []string{"max", "ultra"} {
			raw, _ := json.Marshal(object{"model": model, "input": "fixture", "reasoning": object{"effort": effort}})
			prepared, b, err := Prepare(raw, "fixture", nil)
			if err != nil {
				t.Fatal("model unexpectedly rejected by a local catalog", err)
			}
			var out object
			if err := decode(prepared, &out); err != nil {
				t.Fatal(err)
			}
			if b.Effort != "xhigh" || out["model"] != model || out["reasoning_effort"] != "xhigh" {
				t.Fatal("legacy mapping changed")
			}
		}
	}
}
