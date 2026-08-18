package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name string
		att  attachment
		want docFormat
	}{
		{name: "media type wins", att: attachment{MediaType: "application/pdf", Name: "report.bin"}, want: formatPDF},
		{name: "extension when no media type", att: attachment{Name: "report.docx"}, want: formatDOCX},
		{name: "odt", att: attachment{MediaType: "application/vnd.oasis.opendocument.text"}, want: formatODT},
		{name: "csv", att: attachment{Name: "export.csv"}, want: formatCSV},
		{name: "tsv reads as csv", att: attachment{Name: "export.tsv"}, want: formatCSV},
		{name: "markdown is text", att: attachment{Name: "notes.md"}, want: formatText},
		{name: "any text subtype", att: attachment{MediaType: "text/x-python"}, want: formatText},
		{name: "image unsupported", att: attachment{MediaType: "image/png", Name: "scan.png"}, want: formatUnsupported},
		{name: "archive unsupported", att: attachment{Name: "bundle.zip"}, want: formatUnsupported},
		{name: "nothing to go on", att: attachment{}, want: formatUnsupported},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := detectFormat(test.att); got != test.want {
				t.Errorf("detectFormat() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExtractText_Formats(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		media   string
	}{
		{name: "pdf", fixture: "sample.pdf", media: "application/pdf"},
		{name: "docx", fixture: "sample.docx", media: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{name: "odt", fixture: "sample.odt", media: "application/vnd.oasis.opendocument.text"},
		{name: "csv", fixture: "sample.csv", media: "text/csv"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			att := attachment{Name: test.fixture, MediaType: test.media, Data: readFixture(t, test.fixture)}

			text, truncated, err := extractText(att, defaultMaxAttachmentBytes, defaultMaxAttachmentChars)
			if err != nil {
				t.Fatalf("extractText: %v", err)
			}
			if truncated {
				t.Errorf("small fixture reported as truncated")
			}
			for _, want := range []string{"Jean Dupont", "jean.dupont@example.com"} {
				if !strings.Contains(text, want) {
					t.Errorf("extracted text misses %q: %q", want, text)
				}
			}
		})
	}
}

func TestExtractText_PlainText(t *testing.T) {
	att := attachment{Name: "notes.txt", MediaType: "text/plain", Data: []byte("Jean Dupont habite à Nantes.")}

	text, _, err := extractText(att, defaultMaxAttachmentBytes, defaultMaxAttachmentChars)
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if text != "Jean Dupont habite à Nantes." {
		t.Errorf("text = %q", text)
	}
}

func TestExtractText_Truncation(t *testing.T) {
	att := attachment{Name: "notes.txt", MediaType: "text/plain", Data: []byte("ééééééééé")}

	text, truncated, err := extractText(att, 0, 4)
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if !truncated {
		t.Errorf("truncated = false, want true")
	}
	// The cut counts runes, not bytes, and never splits one in half.
	if text != "éééé" {
		t.Errorf("text = %q, want éééé", text)
	}
}

func TestExtractText_Refusals(t *testing.T) {
	tests := []struct {
		name     string
		att      attachment
		maxBytes int
		wantErr  error
		wantMsg  string
	}{
		{
			name:     "too large",
			att:      attachment{Name: "big.txt", MediaType: "text/plain", Data: []byte("0123456789")},
			maxBytes: 4,
			wantErr:  errTooLarge,
		},
		{
			name:    "scanned pdf yields no text",
			att:     attachment{Name: "scan.pdf", MediaType: "application/pdf"},
			wantErr: errNoText,
		},
		{
			name:    "unsupported format",
			att:     attachment{Name: "photo.png", MediaType: "image/png", Data: []byte("\x89PNG")},
			wantMsg: "unsupported format",
		},
		{
			name:    "corrupted document",
			att:     attachment{Name: "broken.pdf", MediaType: "application/pdf", Data: []byte("not a pdf at all")},
			wantMsg: "open pdf document",
		},
		{
			name:    "invalid utf-8",
			att:     attachment{Name: "notes.txt", MediaType: "text/plain", Data: []byte{0xff, 0xfe, 0xfd}},
			wantMsg: "not valid UTF-8",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			att := test.att
			if test.name == "scanned pdf yields no text" {
				att.Data = readFixture(t, "empty.pdf")
			}

			_, _, err := extractText(att, test.maxBytes, defaultMaxAttachmentChars)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Errorf("error = %v, want %v", err, test.wantErr)
			}
			if test.wantMsg != "" && !strings.Contains(err.Error(), test.wantMsg) {
				t.Errorf("error = %v, want it to mention %q", err, test.wantMsg)
			}
		})
	}
}

func TestExtractText_LeavesNoTemporaryFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	att := attachment{Name: "sample.pdf", MediaType: "application/pdf", Data: readFixture(t, "sample.pdf")}
	if _, _, err := extractText(att, 0, defaultMaxAttachmentChars); err != nil {
		t.Fatalf("extractText: %v", err)
	}
	// Same on the failure path: a document that cannot be parsed still gets
	// written to disk before the walker rejects it.
	broken := attachment{Name: "broken.pdf", MediaType: "application/pdf", Data: []byte("not a pdf")}
	if _, _, err := extractText(broken, 0, defaultMaxAttachmentChars); err == nil {
		t.Fatalf("expected the broken document to fail")
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("attachment bytes left behind in %s: %v", tmp, entries)
	}
}
