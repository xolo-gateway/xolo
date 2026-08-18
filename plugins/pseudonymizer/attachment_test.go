package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestExtractAttachment_ProviderShapes(t *testing.T) {
	pdfBytes := []byte("%PDF-1.4 fake")
	b64 := base64.StdEncoding.EncodeToString(pdfBytes)

	tests := []struct {
		name      string
		part      map[string]any
		wantName  string
		wantMedia string
		wantData  []byte
	}{
		{
			name: "openai chat completions file",
			part: map[string]any{
				"type": "file",
				"file": map[string]any{
					"filename":  "rapport.pdf",
					"file_data": "data:application/pdf;base64," + b64,
				},
			},
			wantName: "rapport.pdf", wantMedia: "application/pdf", wantData: pdfBytes,
		},
		{
			name: "openai responses input_file",
			part: map[string]any{
				"type":      "input_file",
				"filename":  "rapport.pdf",
				"file_data": "data:application/pdf;base64," + b64,
			},
			wantName: "rapport.pdf", wantMedia: "application/pdf", wantData: pdfBytes,
		},
		{
			name: "anthropic base64 document",
			part: map[string]any{
				"type": "document",
				"source": map[string]any{
					"type":       "base64",
					"media_type": "application/pdf",
					"data":       b64,
				},
			},
			wantMedia: "application/pdf", wantData: pdfBytes,
		},
		{
			name: "anthropic plain text document",
			part: map[string]any{
				"type": "document",
				"source": map[string]any{
					"type": "text",
					"data": "Jean Dupont",
				},
			},
			wantMedia: "text/plain", wantData: []byte("Jean Dupont"),
		},
		{
			name: "image data uri",
			part: map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG"))},
			},
			wantMedia: "image/png", wantData: []byte("\x89PNG"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			att := extractAttachment(test.part, defaultMaxAttachmentBytes)

			if att.Name != test.wantName {
				t.Errorf("Name = %q, want %q", att.Name, test.wantName)
			}
			if att.MediaType != test.wantMedia {
				t.Errorf("MediaType = %q, want %q", att.MediaType, test.wantMedia)
			}
			if string(att.Data) != string(test.wantData) {
				t.Errorf("Data = %q, want %q", att.Data, test.wantData)
			}
		})
	}
}

func TestExtractAttachment_ByReferenceCarriesNoData(t *testing.T) {
	// A file the plugin cannot read is a file it cannot vouch for. Each of
	// these must come back empty so the caller refuses it.
	parts := []map[string]any{
		{"type": "file", "file": map[string]any{"file_id": "file-abc123"}},
		{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/photo.png"}},
		{"type": "input_file", "filename": "rapport.pdf", "file_url": "https://example.com/rapport.pdf"},
		{"type": "document", "source": map[string]any{"type": "url", "url": "https://example.com/a.pdf"}},
	}

	for _, part := range parts {
		att := extractAttachment(part, defaultMaxAttachmentBytes)
		if len(att.Data) != 0 {
			t.Errorf("part %v yielded %d bytes, want none", part, len(att.Data))
		}
	}
}

func TestExtractAttachment_OversizedPayloadIsNotDecoded(t *testing.T) {
	// 3 KiB of payload against a 1 KiB limit. The size is judged on the encoded
	// form so the bytes are never materialized.
	big := base64.StdEncoding.EncodeToString(make([]byte, 3*1024))

	parts := []map[string]any{
		{"type": "file", "file": map[string]any{"filename": "big.pdf", "file_data": "data:application/pdf;base64," + big}},
		{"type": "input_file", "filename": "big.pdf", "file_data": "data:application/pdf;base64," + big},
		{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": big}},
		{"type": "document", "source": map[string]any{"type": "text", "data": strings.Repeat("a", 3*1024)}},
	}

	for _, part := range parts {
		att := extractAttachment(part, 1024)
		if !att.Oversized {
			t.Errorf("part %v: Oversized = false, want true", part["type"])
		}
		if len(att.Data) != 0 {
			t.Errorf("part %v: %d bytes decoded despite the limit", part["type"], len(att.Data))
		}
	}

	// And the refusal is reported as a size problem, not as a missing payload.
	cfg := defaultConfig()
	cfg.MaxAttachmentBytes = 1024
	_, _, err := attachmentText(cfg, extractAttachment(parts[0], cfg.MaxAttachmentBytes))
	if got := attachmentReason(err); got != reasonTooLarge {
		t.Errorf("reason = %q, want %q (err: %v)", got, reasonTooLarge, err)
	}
}

func TestDecodeDataURI(t *testing.T) {
	tests := []struct {
		name      string
		uri       string
		wantMedia string
		wantData  string
		wantErr   bool
	}{
		{name: "base64", uri: "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("bonjour")), wantMedia: "text/plain", wantData: "bonjour"},
		{name: "plain payload", uri: "data:text/plain,bonjour", wantMedia: "text/plain", wantData: "bonjour"},
		{name: "no media type", uri: "data:;base64," + base64.StdEncoding.EncodeToString([]byte("x")), wantData: "x"},
		{name: "not a data uri", uri: "https://example.com/a.pdf", wantErr: true},
		{name: "no comma", uri: "data:text/plain;base64", wantErr: true},
		{name: "bad base64", uri: "data:text/plain;base64,!!!!", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			media, data, err := decodeDataURI(test.uri)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got media=%q data=%q", media, data)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeDataURI: %v", err)
			}
			if media != test.wantMedia {
				t.Errorf("media = %q, want %q", media, test.wantMedia)
			}
			if string(data) != test.wantData {
				t.Errorf("data = %q, want %q", data, test.wantData)
			}
		})
	}
}

func TestAttachmentText_Disabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.ProcessAttachments = false

	_, _, err := attachmentText(cfg, attachment{Name: "a.csv", MediaType: "text/csv", Data: []byte("a,b\n1,2\n")})
	if err == nil {
		t.Fatalf("expected an error when attachment processing is off")
	}
	if got := attachmentReason(err); got != reasonDisabled {
		t.Errorf("reason = %q, want %q", got, reasonDisabled)
	}
}

func TestAttachmentReason(t *testing.T) {
	cfg := defaultConfig()

	tests := []struct {
		name string
		att  attachment
		want string
	}{
		{name: "no inline data", att: attachment{Name: "a.pdf", MediaType: "application/pdf"}, want: reasonNoInlineData},
		{name: "unsupported", att: attachment{Name: "a.png", MediaType: "image/png", Data: []byte("\x89PNG")}, want: reasonUnsupported},
		{name: "unreadable", att: attachment{Name: "a.pdf", MediaType: "application/pdf", Data: []byte("nope")}, want: reasonUnreadable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := attachmentText(cfg, test.att)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if got := attachmentReason(err); got != test.want {
				t.Errorf("reason = %q, want %q (err: %v)", got, test.want, err)
			}
		})
	}
}

func TestAttachmentTextPart(t *testing.T) {
	att := attachment{Name: "rapport.pdf", MediaType: "application/pdf"}

	part := attachmentTextPart(att, "file", "[PERSON_1] habite à [LOCATION_1].", false)
	if !strings.Contains(part, "rapport.pdf") || !strings.Contains(part, "[PERSON_1]") {
		t.Errorf("part does not carry the file name and its text: %q", part)
	}
	if strings.Contains(part, "tronqué") {
		t.Errorf("untruncated document reported as truncated: %q", part)
	}

	truncatedPart := attachmentTextPart(att, "file", "[PERSON_1]", true)
	if !strings.Contains(truncatedPart, "tronqué") {
		t.Errorf("truncation not reported to the model: %q", truncatedPart)
	}
}

func TestBlockedAttachmentsReason(t *testing.T) {
	reason := blockedAttachmentsReason([]removedPart{
		{Role: "user", Type: "image_url", Name: "photo.png", Reason: reasonUnsupported},
		{Role: "user", Type: "file", Reason: reasonNoInlineData},
	})

	for _, want := range []string{"photo.png", reasonUnsupported, "pièce jointe sans nom", reasonNoInlineData} {
		if !strings.Contains(reason, want) {
			t.Errorf("rejection reason misses %q: %q", want, reason)
		}
	}
}
