package sanitize

import (
	"encoding/json"
	"testing"
)

func body(t *testing.T, tools ...any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"model":  "gpt-6-astra",
		"stream": true,
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "hi"}},
		}},
		"tools": tools,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func toolNames(t *testing.T, raw []byte) []string {
	t.Helper()
	var doc struct {
		Tools []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := make([]string, 0, len(doc.Tools))
	for _, tl := range doc.Tools {
		if tl.Name != "" {
			out = append(out, tl.Name)
		} else {
			out = append(out, tl.Type)
		}
	}
	return out
}

// Codex advertises web_search with external_web_access set by default. Dropping
// it is the whole reason this package exists.
func TestStripsCodexDefaultWebSearch(t *testing.T) {
	raw := body(t,
		map[string]any{"type": "function", "name": "shell_command", "description": "run"},
		map[string]any{"type": "web_search", "external_web_access": true},
	)
	out, report, err := Apply(raw)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(report.RemovedServerTools) != 1 || report.RemovedServerTools[0] != "web_search" {
		t.Fatalf("expected web_search removed, got %v", report.RemovedServerTools)
	}
	names := toolNames(t, out)
	if len(names) != 1 || names[0] != "shell_command" {
		t.Fatalf("expected only shell_command to survive, got %v", names)
	}
}

// Client-side tools are the point of the bridge; they must never be dropped.
func TestKeepsClientTools(t *testing.T) {
	raw := body(t,
		map[string]any{"type": "function", "name": "apply_patch", "description": "patch"},
		map[string]any{"type": "namespace", "name": "mcp__codex_apps__gmail"},
		map[string]any{"type": "function", "name": "update_plan"},
	)
	out, report, err := Apply(raw)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !report.Empty() {
		t.Fatalf("expected no removals, got %v", report.RemovedServerTools)
	}
	names := toolNames(t, out)
	want := []string{"apply_patch", "mcp__codex_apps__gmail", "update_plan"}
	if len(names) != len(want) {
		t.Fatalf("tool count changed: got %v want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tool order/content changed: got %v want %v", names, want)
		}
	}
}

func TestStripsImageGeneration(t *testing.T) {
	raw := body(t,
		map[string]any{"type": "function", "name": "shell"},
		map[string]any{"type": "image_generation"},
	)
	_, report, err := Apply(raw)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(report.RemovedServerTools) != 1 || report.RemovedServerTools[0] != "image_generation" {
		t.Fatalf("got %v", report.RemovedServerTools)
	}
}

// An empty tools array is rejected upstream, so the key must go entirely.
func TestRemovesToolsKeyWhenEverythingIsStripped(t *testing.T) {
	raw := body(t, map[string]any{"type": "web_search", "external_web_access": true})
	out, _, err := Apply(raw)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := doc["tools"]; present {
		t.Fatalf("tools key should be absent when empty, body=%s", out)
	}
}

// tool_choice naming a removed tool would 400 upstream.
func TestRewritesToolChoiceNamingRemovedTool(t *testing.T) {
	raw := body(t, map[string]any{"type": "function", "name": "shell"})
	withChoice, err := setChoice(raw, map[string]any{"type": "web_search"})
	if err != nil {
		t.Fatalf("setChoice: %v", err)
	}
	out, report, err := Apply(withChoice)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.ForcedToolChoice != "auto" {
		t.Fatalf("expected tool_choice forced to auto, got %q", report.ForcedToolChoice)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %v, want auto", doc["tool_choice"])
	}
}

func TestLeavesValidToolChoiceAlone(t *testing.T) {
	raw := body(t, map[string]any{"type": "function", "name": "shell"})
	withChoice, err := setChoice(raw, map[string]any{"type": "function", "name": "shell"})
	if err != nil {
		t.Fatalf("setChoice: %v", err)
	}
	_, report, err := Apply(withChoice)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.ForcedToolChoice != "" {
		t.Fatalf("tool_choice should be untouched, got %q", report.ForcedToolChoice)
	}
}

func TestNoToolsKeyIsUntouched(t *testing.T) {
	raw := []byte(`{"model":"gpt-6-astra","stream":true,"input":"hello"}`)
	out, report, err := Apply(raw)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !report.Empty() {
		t.Fatalf("unexpected report: %v", report)
	}
	if string(out) != string(raw) {
		t.Fatalf("body changed:\n got %s\nwant %s", out, raw)
	}
}

func TestRejectsInvalidJSON(t *testing.T) {
	if _, _, err := Apply([]byte(`{not json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func setChoice(raw []byte, choice map[string]any) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	doc["tool_choice"] = choice
	return json.Marshal(doc)
}
