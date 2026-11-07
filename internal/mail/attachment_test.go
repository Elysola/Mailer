package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

// singlePart and multiPart build the BODYSTRUCTURE values a server would send,
// so these tests exercise the filtering rules without an IMAP connection.
func singlePart(typ, subtype string, size uint32, filename, contentID, disposition string) *imap.BodyStructureSinglePart {
	sp := &imap.BodyStructureSinglePart{
		Type:    typ,
		Subtype: subtype,
		Size:    size,
		Params:  map[string]string{},
		ID:      contentID,
	}
	if filename != "" {
		sp.Params["name"] = filename
	}
	if disposition != "" {
		sp.Extended = &imap.BodyStructureSinglePartExt{
			Disposition: &imap.BodyStructureDisposition{
				Value:  disposition,
				Params: map[string]string{"filename": filename},
			},
		}
	}
	return sp
}

func multiPart(subtype string, children ...imap.BodyStructure) *imap.BodyStructureMultiPart {
	return &imap.BodyStructureMultiPart{Subtype: subtype, Children: children}
}

func TestAttachmentsFromStructureNil(t *testing.T) {
	if got := attachmentsFromStructure(nil); got != nil {
		t.Errorf("nil structure = %v, want nil", got)
	}
}

func TestAttachmentsFromStructureSkipsBodyParts(t *testing.T) {
	// A plain message with no attachments must not sprout any: the body itself
	// is a MIME part and is the classic false positive here.
	bs := multiPart("mixed",
		singlePart("TEXT", "PLAIN", 120, "", "", ""),
		singlePart("TEXT", "HTML", 400, "", "", ""),
	)
	if got := attachmentsFromStructure(bs); got != nil {
		t.Fatalf("attachments = %v, want none", got)
	}
}

func TestAttachmentsFromStructureSinglePDF(t *testing.T) {
	bs := multiPart("mixed",
		singlePart("TEXT", "PLAIN", 120, "", "", ""),
		singlePart("APPLICATION", "PDF", 12345, "invoice.pdf", "", "attachment"),
	)
	got := attachmentsFromStructure(bs)
	if len(got) != 1 {
		t.Fatalf("attachments = %v, want exactly 1", got)
	}
	a := got[0]
	if a.Filename != "invoice.pdf" {
		t.Errorf("Filename = %q, want invoice.pdf", a.Filename)
	}
	if a.ContentType != "application/pdf" {
		t.Errorf("ContentType = %q, want application/pdf", a.ContentType)
	}
	if a.Size != 12345 {
		t.Errorf("Size = %d, want 12345", a.Size)
	}
	if a.Inline {
		t.Error("Inline = true for an attachment disposition")
	}
}

func TestAttachmentsFromStructureSkipsUnnamedInlineImages(t *testing.T) {
	// An unnamed inline image with a content id is referenced from the HTML
	// body (a logo, a signature graphic). Listing it as an attachment would be
	// noise on nearly every marketing mail.
	bs := multiPart("related",
		singlePart("TEXT", "HTML", 400, "", "", ""),
		singlePart("IMAGE", "PNG", 900, "", "<logo123>", "inline"),
	)
	if got := attachmentsFromStructure(bs); got != nil {
		t.Fatalf("attachments = %v, want none", got)
	}
}

func TestAttachmentsFromStructureKeepsNamedInline(t *testing.T) {
	// A named inline part is a real attachment: mail clients list it.
	bs := multiPart("mixed",
		singlePart("TEXT", "PLAIN", 10, "", "", ""),
		singlePart("IMAGE", "JPEG", 2048, "photo.jpg", "", "inline"),
	)
	got := attachmentsFromStructure(bs)
	if len(got) != 1 || got[0].Filename != "photo.jpg" {
		t.Fatalf("attachments = %v, want photo.jpg", got)
	}
	if !got[0].Inline {
		t.Error("Inline = false for an inline disposition")
	}
}

func TestAttachmentsFromStructureKeepsUnnamedWithDisposition(t *testing.T) {
	// Explicit attachment disposition wins over the missing-filename heuristics:
	// the sender said it is an attachment, so report it even unnamed.
	bs := multiPart("mixed",
		singlePart("TEXT", "PLAIN", 10, "", "", ""),
		singlePart("APPLICATION", "OCTET-STREAM", 77, "", "", "attachment"),
	)
	got := attachmentsFromStructure(bs)
	if len(got) != 1 {
		t.Fatalf("attachments = %v, want 1", got)
	}
	if got[0].Filename != "" {
		t.Errorf("Filename = %q, want empty", got[0].Filename)
	}
}

func TestAttachmentsFromStructureMultipleAndNested(t *testing.T) {
	bs := multiPart("mixed",
		singlePart("TEXT", "PLAIN", 10, "", "", ""),
		multiPart("alternative",
			singlePart("TEXT", "HTML", 20, "", "", ""),
			singlePart("APPLICATION", "ZIP", 5000, "a.zip", "", "attachment"),
		),
		singlePart("IMAGE", "PNG", 300, "b.png", "", "attachment"),
	)
	got := attachmentsFromStructure(bs)
	if len(got) != 2 {
		t.Fatalf("attachments = %v, want 2 (a.zip, b.png)", got)
	}
	if got[0].Filename != "a.zip" || got[1].Filename != "b.png" {
		t.Errorf("filename order = %q, %q; want a.zip, b.png (DFS order)", got[0].Filename, got[1].Filename)
	}
}

func TestDecodeMIMEWord(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"plain.pdf":                    "plain.pdf",
		"=?utf-8?B?5oql5ZGKLnBkZg==?=": "报告.pdf",
	}
	for in, want := range cases {
		if got := decodeMIMEWord(in); got != want {
			t.Errorf("decodeMIMEWord(%q) = %q, want %q", in, got, want)
		}
	}
}
