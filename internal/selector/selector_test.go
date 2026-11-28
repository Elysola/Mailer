package selector

import (
	"testing"

	"github.com/elysola/mailer/internal/core"
)

func testEnvelope() core.Envelope {
	msg := core.Message{
		From:    "GitHub <notifications@github.com>",
		Subject: "[alert] verification code",
		Source:  "work-gmail",
		Preview: "Your code is 690831",
		Headers: map[string]string{"X-Spam-Flag": "YES"},
	}
	env := core.NewEnvelope(msg)
	env.Fields["class"] = "notice"
	return env
}

func TestSelectorBasicComparisons(t *testing.T) {
	env := testEnvelope()
	cases := []struct {
		expr string
		want bool
	}{
		{`source == "work-gmail"`, true},
		{`source == "other"`, false},
		{`source != "other"`, true},
		{`subject contains "verification"`, true},
		{`subject contains "invoice"`, false},
		{`from =~ "@github\x2ecom"`, true},
		{`from !~ "@gitlab\x2ecom"`, true},
		{`fields.class == "notice"`, true},
		{`fields.class == "bill"`, false},
		{`fields.missing == "x"`, false},       // missing reference fails closed
		{`headers.x-spam-flag == "YES"`, true}, // case-insensitive header lookup
	}
	for _, tc := range cases {
		if got := MatchSelector(tc.expr, env); got != tc.want {
			t.Errorf("MatchSelector(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestSelectorBooleanCombinators(t *testing.T) {
	env := testEnvelope()
	cases := []struct {
		expr string
		want bool
	}{
		{`source == "work-gmail" && subject contains "verification"`, true},
		{`source == "work-gmail" && subject contains "invoice"`, false},
		{`source == "other" || subject contains "verification"`, true},
		{`!(source == "other")`, true},
		{`!(source == "work-gmail")`, false},
		{`(source == "other" || source == "work-gmail") && fields.class == "notice"`, true},
		// && binds tighter than ||:
		{`source == "other" || source == "work-gmail" && fields.class == "bill"`, false},
	}
	for _, tc := range cases {
		if got := MatchSelector(tc.expr, env); got != tc.want {
			t.Errorf("MatchSelector(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestSelectorPredicates(t *testing.T) {
	env := testEnvelope()
	if !MatchSelector(`has(fields.class)`, env) {
		t.Error("has(fields.class) should be true")
	}
	if MatchSelector(`has(fields.missing)`, env) {
		t.Error("has(fields.missing) should be false")
	}
	if !MatchSelector(`present(subject)`, env) {
		t.Error("present(subject) should be true")
	}
	if MatchSelector(`has(attachments)`, env) {
		t.Error("has(attachments) should be false without attachments")
	}
	env.Message.Attachments = []core.Attachment{{Filename: "x.pdf"}}
	if !MatchSelector(`has(attachments)`, env) {
		t.Error("has(attachments) should be true with attachments")
	}
}

func TestSelectorFailsClosed(t *testing.T) {
	env := testEnvelope()
	// Syntax garbage must never match: failing open would spam every notifier.
	for _, bad := range []string{"", "&&", "source ==", `source == "unterminated`, "((", "has(", "unknown_op x"} {
		if MatchSelector(bad, env) {
			t.Errorf("MatchSelector(%q) should fail closed", bad)
		}
	}
}

func TestCheckSelector(t *testing.T) {
	if err := CheckSelector(`source == "x" && has(fields.y)`); err != nil {
		t.Errorf("valid expression rejected: %v", err)
	}
	for _, bad := range []string{"", "&&", "((", `source ==`} {
		if err := CheckSelector(bad); err == nil {
			t.Errorf("CheckSelector(%q) should reject", bad)
		}
	}
}

func TestSelectorBareTruthiness(t *testing.T) {
	env := testEnvelope()
	if !MatchSelector(`fields.class`, env) {
		t.Error("bare fields.class should be truthy when non-empty")
	}
	if MatchSelector(`fields.missing`, env) {
		t.Error("bare fields.missing should be false")
	}
}
