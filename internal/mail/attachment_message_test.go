package mail

import (
	"strings"
	"testing"
)

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:               "0 B",
		999:             "999 B",
		1024:            "1.0 KB",
		1536:            "1.5 KB",
		10 * 1024:       "10 KB",
		5 * 1024 * 1024: "5.0 MB",
	}
	for in, want := range cases {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestAttachmentSummaryEmpty(t *testing.T) {
	var m Message
	if got := m.AttachmentSummary(); got != "" {
		t.Errorf("AttachmentSummary = %q, want empty", got)
	}
}

func TestAttachmentSummaryListsNameAndSize(t *testing.T) {
	m := Message{Attachments: []Attachment{
		{Filename: "invoice.pdf", ContentType: "application/pdf", Size: 2048},
		{Filename: "photo.jpg", Size: 5 * 1024 * 1024},
	}}
	got := m.AttachmentSummary()
	for _, want := range []string{"invoice.pdf (2.0 KB)", "photo.jpg (5.0 MB)"} {
		if !strings.Contains(got, want) {
			t.Errorf("AttachmentSummary = %q, missing %q", got, want)
		}
	}
}

func TestAttachmentSummaryFallsBackToContentType(t *testing.T) {
	m := Message{Attachments: []Attachment{{ContentType: "application/pdf", Size: 10}}}
	if got := m.AttachmentSummary(); !strings.Contains(got, "application/pdf") {
		t.Errorf("AttachmentSummary = %q, want the content type as fallback", got)
	}
}

func TestAttachmentSummaryTruncatesLongNames(t *testing.T) {
	// A hostile 300-character filename must not push the mail out of the
	// notification.
	m := Message{Attachments: []Attachment{{Filename: strings.Repeat("x", 300), Size: 1}}}
	got := m.AttachmentSummary()
	if r := []rune(got); len(r) > 90 {
		t.Errorf("AttachmentSummary length %d, want bounded: %q", len(r), got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("AttachmentSummary = %q, want an ellipsis", got)
	}
}

func TestTextIncludesAttachmentLine(t *testing.T) {
	m := Message{
		Account:     "primary",
		From:        "a@b.c",
		Subject:     "hi",
		Preview:     "body",
		Attachments: []Attachment{{Filename: "a.pdf", Size: 1024}},
	}
	got := m.Text()
	if !strings.Contains(got, "Attachments: a.pdf (1.0 KB)") {
		t.Errorf("Text = %q, missing attachments line", got)
	}
	// The attachment line belongs above the preview, not after it.
	if strings.Index(got, "Attachments:") > strings.Index(got, "body") {
		t.Errorf("Text = %q, attachments line should precede the preview", got)
	}
}

func TestTextOmitsAttachmentLineWhenNone(t *testing.T) {
	m := Message{Account: "primary", Subject: "hi", Preview: "body"}
	if strings.Contains(m.Text(), "Attachments:") {
		t.Error("Text has an attachments line for a message without attachments")
	}
}
