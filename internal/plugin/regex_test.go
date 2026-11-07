package plugin

import "testing"

func TestCompilePatternsRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name    string
		pattern Pattern
	}{
		{"missing name", Pattern{Regexp: `\d+`}},
		{"missing regexp", Pattern{Name: "code"}},
		{"bad regexp", Pattern{Name: "code", Regexp: `(\d+`}},
		{"group out of range", Pattern{Name: "code", Regexp: `(\d+)`, Group: 2}},
		{"negative group", Pattern{Name: "code", Regexp: `(\d+)`, Group: -1}},
		{"unknown source", Pattern{Name: "code", Regexp: `\d+`, Source: "body"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := compilePatterns([]Pattern{tc.pattern}); err == nil {
				t.Fatalf("expected error for %+v", tc.pattern)
			}
		})
	}
}

func TestApplyPatternsAcrossSources(t *testing.T) {
	patterns, err := compilePatterns([]Pattern{
		{Name: "code", Regexp: `\b(\d{6})\b`, Group: 1},
		{Name: "repo", Regexp: `([\w.-]+/[\w.-]+)#(\d+)`, Source: "subject", Group: 0},
		{Name: "pr", Regexp: `([\w.-]+/[\w.-]+)#(\d+)`, Source: "subject", Group: 2},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	msg := &Mail{
		Text:    "您的验证码是 690831，5 分钟内有效",
		Subject: "PR opened: acme/widgets#42",
	}
	fields, err := applyPatterns(patterns, msg)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if fields["code"] != "690831" {
		t.Errorf("code = %q, want 690831", fields["code"])
	}
	if fields["repo"] != "acme/widgets#42" {
		t.Errorf("repo = %q, want acme/widgets#42", fields["repo"])
	}
	if fields["pr"] != "42" {
		t.Errorf("pr = %q, want 42", fields["pr"])
	}
}

func TestApplyPatternsMissingIsNotAnError(t *testing.T) {
	patterns, err := compilePatterns([]Pattern{{Name: "code", Regexp: `\b\d{6}\b`}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	fields, err := applyPatterns(patterns, &Mail{Text: "no code here"})
	if err != nil {
		t.Fatalf("optional miss must not error: %v", err)
	}
	if _, ok := fields["code"]; ok {
		t.Errorf("code unexpectedly captured: %v", fields)
	}
}

func TestApplyPatternsRequiredMissErrors(t *testing.T) {
	patterns, err := compilePatterns([]Pattern{
		{Name: "code", Regexp: `\b\d{6}\b`, Required: true},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := applyPatterns(patterns, &Mail{Text: "no code"}); err == nil {
		t.Fatal("expected error for unmet required pattern")
	}
}

func TestMergePatternFieldsPluginWins(t *testing.T) {
	patterns, err := compilePatterns([]Pattern{{Name: "code", Regexp: `\b\d{6}\b`}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	msg := &Mail{Text: "code 111111"}
	result := &Result{Matched: true, Fields: map[string]string{"code": "999999"}}
	if err := mergePatternFields(result, patterns, msg); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if result.Fields["code"] != "999999" {
		t.Errorf("plugin field overwritten: %q", result.Fields["code"])
	}

	// No plugin fields: the pattern fills the gap.
	empty := &Result{Matched: true}
	if err := mergePatternFields(empty, patterns, msg); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if empty.Fields["code"] != "111111" {
		t.Errorf("pattern value not merged: %v", empty.Fields)
	}
}
