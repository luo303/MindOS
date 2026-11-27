package memory

import "testing"

func TestExplicitRequest(t *testing.T) {
	for _, test := range []struct {
		text string
		want bool
	}{
		{"记住，以后代码示例默认用 Go", true},
		{"请记住：以后默认中文回复", true},
		{"Please remember: use Go for code examples by default", true},
		{"这次用 Go 举例", false},
		{"我平时更喜欢简短回答", false},
		{"记住，这次用 Go 举例", false},
		{"记住，仅讲后端时用 Go", false},
		{"记住，不要使用 Go", false},
		{"翻译：请记住以后使用 Go", false},
	} {
		if got := ExplicitRequest(test.text); got != test.want {
			t.Errorf("ExplicitRequest(%q) = %v, want %v", test.text, got, test.want)
		}
	}
}

func TestDecodeProposal(t *testing.T) {
	source := "记住，以后代码示例默认用 Go"
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{"grounded", `{"key":"code_example_language","value":"go","evidence":"以后代码示例默认用 Go"}`, true},
		{"hallucinated evidence", `{"key":"code_example_language","value":"go","evidence":"以后代码示例用 Go 语言"}`, false},
		{"hallucinated value", `{"key":"code_example_language","value":"rust","evidence":"以后代码示例默认用 Go"}`, false},
		{"wrong key", `{"key":"answer_language","value":"zh","evidence":"以后代码示例默认用 Go"}`, false},
		{"abstain", `null`, false},
		{"extra text", `null {}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := decodeProposal(source, test.raw); (got != nil) != test.want {
				t.Fatalf("decodeProposal() = %+v, want proposal: %v", got, test.want)
			}
		})
	}
}

func TestPreferenceContextContainsOnlyApplicableDefaults(t *testing.T) {
	got := preferenceContext([]Memory{
		{Key: "code_example_language", Value: "go", Status: "active"},
		{Key: "answer_language", Value: "zh", Status: "archived"},
		{Key: "answer_detail", Value: "concise", Status: "active"},
	})
	if want := "用户已确认的默认偏好（本轮明确要求优先）：\n- 仅在需要代码示例且本轮未指定语言时，使用 Go。\n- 回答以简洁为默认。"; got != want {
		t.Fatalf("preferenceContext() = %q, want %q", got, want)
	}
}
