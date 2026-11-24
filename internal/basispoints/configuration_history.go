package basispoints

import "fmt"

// Only known reasoning metadata can be consumed locally. Other configuration
// changes need explicit translation rather than being silently discarded.
func historyConfigurationEffort(item object) (string, error) {
	for key := range item {
		if key != "type" && key != "reasoning" {
			return "", fmt.Errorf("unsupported history configuration field")
		}
	}
	reasoning, ok := item["reasoning"].(object)
	if !ok {
		return "", fmt.Errorf("history configuration requires reasoning")
	}
	for key := range reasoning {
		if key != "effort" && key != "mode" {
			return "", fmt.Errorf("unsupported history reasoning field")
		}
	}
	if mode, exists := reasoning["mode"]; exists && mode != "standard" {
		return "", fmt.Errorf("unsupported history reasoning mode")
	}
	effort, ok := reasoning["effort"].(string)
	if !ok || effort == "" {
		return "", fmt.Errorf("history configuration requires reasoning effort")
	}
	// Validate the historical vocabulary without applying the old model's
	// downgrade mapping. The effective request model normalizes it once, after
	// current-vs-historical precedence has been resolved in PrepareWithOptions.
	if _, err := NormalizeEffort(effort); err != nil {
		return "", err
	}
	return effort, nil
}
