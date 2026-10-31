package openai

import "testing"

func TestNormalizeEffort(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"low":      "low",
		"LOW":      "low",
		"minimal":  "low",
		"fast":     "low",
		"低":        "low",
		"high":     "high",
		"deep":     "high",
		"高":        "high",
		"max":      "max",
		"MAX":      "max",
		"最高":       "max",
		"medium":   "high",
		"中":        "high",
		"nonsense": "",
	}
	for input, want := range cases {
		if got := normalizeEffort(input); got != want {
			t.Errorf("normalizeEffort(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEffortLevelsMatchDeepSeek(t *testing.T) {
	want := []string{"low", "high", "max"}
	if len(EffortLevels) != len(want) {
		t.Fatalf("EffortLevels = %v, want %v", EffortLevels, want)
	}
	for i := range want {
		if EffortLevels[i] != want[i] {
			t.Fatalf("EffortLevels = %v, want %v", EffortLevels, want)
		}
	}
}
