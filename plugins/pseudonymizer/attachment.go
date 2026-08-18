package main

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
)

// attachment is a content part carrying a file, normalized across the part
// shapes the providers use (OpenAI file/input_file, Anthropic document…).
type attachment struct {
	// PartType is the part type as declared in the request, kept for reporting.
	PartType string
	Name     string
	// MediaType is the declared MIME type, empty when the request does not
	// carry one.
	MediaType string
	// Data holds the decoded bytes. Empty when the part references a file the
	// plugin cannot fetch (file_id, remote URL): such a part is unprocessable,
	// which is exactly what the caller needs to know.
	Data []byte
	// Oversized marks a payload left undecoded because it exceeds the limit.
	// The size is read off the encoded form, so an oversized attachment never
	// gets a decoded copy of itself in memory.
	Oversized bool
}

// extractAttachment pulls the inline bytes out of a content part, decoding at
// most maxBytes of payload (0 lifts the limit).
//
// Only inline data is accepted. A part pointing at a file_id or a remote URL
// resolves to an attachment without data: the plugin has no way to read what
// would reach the LLM, so it cannot vouch for it either.
func extractAttachment(partMap map[string]any, maxBytes int) attachment {
	att := attachment{
		PartType: stringField(partMap, "type"),
		Name:     partName(partMap),
	}

	// OpenAI Chat Completions: {"type":"file","file":{"filename":…,"file_data":"data:…"}}
	if file, ok := partMap["file"].(map[string]any); ok {
		att.MediaType, att.Data, att.Oversized = decodeInline(stringField(file, "file_data"), maxBytes)
		return att
	}

	// OpenAI Responses: {"type":"input_file","filename":…,"file_data":"data:…"}
	if raw := stringField(partMap, "file_data"); raw != "" {
		att.MediaType, att.Data, att.Oversized = decodeInline(raw, maxBytes)
		return att
	}

	// Anthropic: {"type":"document","source":{"type":"base64","media_type":…,"data":…}}
	if src, ok := partMap["source"].(map[string]any); ok {
		att.MediaType = stringField(src, "media_type")
		switch stringField(src, "type") {
		case "base64":
			payload := stringField(src, "data")
			if oversizedBase64(payload, maxBytes) {
				att.Oversized = true
				break
			}
			if data, err := base64.StdEncoding.DecodeString(payload); err == nil {
				att.Data = data
			}
		case "text":
			data := stringField(src, "data")
			if maxBytes > 0 && len(data) > maxBytes {
				att.Oversized = true
				break
			}
			att.Data = []byte(data)
			if att.MediaType == "" {
				att.MediaType = "text/plain"
			}
		}
		return att
	}

	// Images are carried as a URL that may itself be a data URI. They are read
	// here so the caller can name the format it is refusing.
	if img, ok := partMap["image_url"].(map[string]any); ok {
		att.MediaType, att.Data, att.Oversized = decodeInline(stringField(img, "url"), maxBytes)
		return att
	}

	return att
}

// decodeInline decodes a data URI, refusing payloads above maxBytes. Anything
// else — a plain URL, an empty field — yields no data.
func decodeInline(raw string, maxBytes int) (mediaType string, data []byte, oversized bool) {
	if !strings.HasPrefix(raw, "data:") {
		return "", nil, false
	}
	if _, payload, found := strings.Cut(raw, ","); found && oversizedBase64(payload, maxBytes) {
		return "", nil, true
	}
	mediaType, data, err := decodeDataURI(raw)
	if err != nil {
		return "", nil, false
	}
	return mediaType, data, false
}

// oversizedBase64 reports whether a base64 payload decodes to more than
// maxBytes, judging from its encoded length: four encoded characters carry
// three bytes. Reading the size before decoding keeps an oversized attachment
// from ever being materialized.
func oversizedBase64(payload string, maxBytes int) bool {
	if maxBytes <= 0 {
		return false
	}
	return len(payload)/4*3 > maxBytes
}

// decodeDataURI splits a RFC 2397 data URI into its media type and bytes.
// Only base64 payloads are decoded; a percent-encoded one is not something
// providers emit for file attachments.
func decodeDataURI(uri string) (string, []byte, error) {
	rest, ok := strings.CutPrefix(uri, "data:")
	if !ok {
		return "", nil, fmt.Errorf("not a data URI")
	}
	header, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return "", nil, fmt.Errorf("malformed data URI")
	}
	mediaType, isBase64 := header, false
	if trimmed, found := strings.CutSuffix(header, ";base64"); found {
		mediaType, isBase64 = trimmed, true
	}
	if !isBase64 {
		return mediaType, []byte(payload), nil
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return mediaType, nil, fmt.Errorf("decode base64 payload: %w", err)
	}
	return mediaType, data, nil
}

// extension returns the lowercased file extension of the attachment name,
// dot included, or an empty string when the name carries none.
func (a attachment) extension() string {
	return strings.ToLower(filepath.Ext(a.Name))
}

// stringField reads a string field from a decoded JSON object.
func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
