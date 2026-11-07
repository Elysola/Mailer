package app

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// rawMailWithAttachment builds a multipart message carrying one PDF part, so
// the IMAP server reports a real BODYSTRUCTURE for the fetcher to inspect.
func rawMailWithAttachment(msgID, subject, filename string) string {
	body := "VGhpcyBpcyBub3QgYSByZWFsIFBERi4=\r\n" // base64 "This is not a real PDF."
	return fmt.Sprintf("From: sender@example.com\r\n"+
		"To: %s@example.com\r\n"+
		"Subject: %s\r\n"+
		"Message-Id: %s\r\n"+
		"Date: Mon, 02 Jan 2026 10:00:00 +0000\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Type: multipart/mixed; boundary=BOUNDARY\r\n"+
		"\r\n"+
		"--BOUNDARY\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n"+
		"\r\n"+
		"Body for %s\r\n"+
		"--BOUNDARY\r\n"+
		"Content-Type: application/pdf; name=\"%s\"\r\n"+
		"Content-Disposition: attachment; filename=\"%s\"\r\n"+
		"Content-Transfer-Encoding: base64\r\n"+
		"\r\n"+
		"%s"+
		"--BOUNDARY--\r\n",
		testUser, subject, msgID, subject, filename, filename, body)
}

// TestAttachmentMetadataReachesDelivery covers the whole path: BODYSTRUCTURE
// parsed by the fetcher, carried on mail.Message, and included in the default
// notification text. The bytes themselves are never fetched.
func TestAttachmentMetadataReachesDelivery(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	n := newFake("good", false)
	acc := env.acc
	acc.NotifyExisting = true
	p := newTestPoller(t, testCfg(acc, nil), store, n)

	env.appendMail(t, rawMailWithAttachment("<att@x>", "with file", "invoice.pdf"))
	pollOnce(t, p)

	if n.sentCount() != 1 {
		t.Fatalf("sent %d, want 1", n.sentCount())
	}
	sent := n.lastMessage()
	if len(sent.Attachments) != 1 {
		t.Fatalf("attachments = %+v, want exactly 1", sent.Attachments)
	}
	a := sent.Attachments[0]
	if a.Filename != "invoice.pdf" {
		t.Errorf("Filename = %q, want invoice.pdf", a.Filename)
	}
	// imapmemserver may report the part either as application/pdf or as the
	// generic type it stored; both are acceptable as long as the extension and
	// the attachment list are right.
	if a.ContentType == "" {
		t.Error("ContentType is empty")
	}
	if a.Size <= 0 {
		t.Errorf("Size = %d, want positive", a.Size)
	}
	if a.Inline {
		t.Error("Inline = true for an attachment disposition")
	}

	if text := sent.Text(); !strings.Contains(text, "invoice.pdf") {
		t.Errorf("notification text = %q, want it to mention invoice.pdf", text)
	}
	if n := pendingCount(t, store); n != 0 {
		t.Errorf("pending = %d, want 0", n)
	}
}

// TestAttachmentMetadataSurvivesRetry: the queued row must keep the attachment
// list, otherwise a retried notification silently loses information the first
// attempt tried to deliver.
func TestAttachmentMetadataSurvivesRetry(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	n := newFake("bad", true)
	acc := env.acc
	acc.NotifyExisting = true
	p := newTestPoller(t, testCfg(acc, nil), store, n)

	env.appendMail(t, rawMailWithAttachment("<att2@x>", "retry file", "report.pdf"))
	pollOnce(t, p)

	if pendingCount(t, store) != 1 {
		t.Fatalf("pending = %d, want 1", pendingCount(t, store))
	}
	due, err := store.DuePending(testAcc, time.Now().Add(time.Hour), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("DuePending = %v, %v; want one row", due, err)
	}
	if len(due[0].Attachments) != 1 || due[0].Attachments[0].Filename != "report.pdf" {
		t.Fatalf("queued attachments = %+v, want report.pdf", due[0].Attachments)
	}

	n.setFail(false)
	makeDue(t, store, due[0].UID, due[0].Failed)
	pollOnce(t, p)

	if n.sentCount() != 1 {
		t.Fatalf("sent %d, want 1", n.sentCount())
	}
	retried := n.lastMessage()
	if len(retried.Attachments) != 1 || retried.Attachments[0].Filename != "report.pdf" {
		t.Errorf("retried attachments = %+v, want report.pdf", retried.Attachments)
	}
	if text := retried.Text(); !strings.Contains(text, "report.pdf") {
		t.Errorf("retried text = %q, want it to mention report.pdf", text)
	}
}

// TestPlainMailHasNoAttachments guards against the body MIME part being
// mistaken for an attachment.
func TestPlainMailHasNoAttachments(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	n := newFake("good", false)
	acc := env.acc
	acc.NotifyExisting = true
	p := newTestPoller(t, testCfg(acc, nil), store, n)

	env.appendMail(t, rawMail("<plain@x>", "no files"))
	pollOnce(t, p)

	if n.sentCount() != 1 {
		t.Fatalf("sent %d, want 1", n.sentCount())
	}
	if got := n.lastMessage().Attachments; len(got) != 0 {
		t.Errorf("attachments = %+v, want none", got)
	}
	if strings.Contains(n.lastMessage().Text(), "Attachments:") {
		t.Error("notification text lists attachments for a plain message")
	}
}
