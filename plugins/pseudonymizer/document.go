package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/bornholm/go-anon/pkg/csv"
	"github.com/bornholm/go-anon/pkg/docprocessor"
	"github.com/bornholm/go-anon/pkg/docx"
	"github.com/bornholm/go-anon/pkg/odt"
	"github.com/bornholm/go-anon/pkg/pdf"
)

// docFormat identifies the document formats the plugin knows how to read.
type docFormat string

const (
	formatPDF  docFormat = "pdf"
	formatDOCX docFormat = "docx"
	formatODT  docFormat = "odt"
	formatCSV  docFormat = "csv"
	formatText docFormat = "text"
	// formatUnsupported covers everything the plugin cannot turn into text:
	// images, audio, archives, proprietary formats, and anything it cannot
	// identify at all.
	formatUnsupported docFormat = ""
)

// errNoText signals a document the walker opened but drew no text from —
// a scanned PDF being the typical case. It is not a supported document: its
// content would reach the LLM unexamined, or not at all.
var errNoText = errors.New("no extractable text")

// errTooLarge signals an attachment above the configured size limit.
var errTooLarge = errors.New("attachment too large")

// mediaTypeFormats maps the MIME types providers send to the reader to use.
var mediaTypeFormats = map[string]docFormat{
	"application/pdf": formatPDF,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": formatDOCX,
	"application/vnd.oasis.opendocument.text":                                 formatODT,
	"text/csv":                  formatCSV,
	"text/tab-separated-values": formatCSV,
	"application/csv":           formatCSV,
	"application/json":          formatText,
	"application/xml":           formatText,
	"application/x-yaml":        formatText,
}

// extensionFormats maps file extensions to the reader to use, for the requests
// that declare no media type or an unhelpfully generic one.
var extensionFormats = map[string]docFormat{
	".pdf":      formatPDF,
	".docx":     formatDOCX,
	".odt":      formatODT,
	".csv":      formatCSV,
	".tsv":      formatCSV,
	".txt":      formatText,
	".md":       formatText,
	".markdown": formatText,
	".json":     formatText,
	".xml":      formatText,
	".yaml":     formatText,
	".yml":      formatText,
	".log":      formatText,
}

// detectFormat resolves the reader to use for an attachment.
//
// The declared media type wins over the extension: it is what the provider
// will act on. text/* falls back to plain text, which covers the long tail of
// text/x-something a filename would not reveal.
func detectFormat(att attachment) docFormat {
	if format, ok := mediaTypeFormats[att.MediaType]; ok {
		return format
	}
	if format, ok := extensionFormats[att.extension()]; ok {
		return format
	}
	if strings.HasPrefix(att.MediaType, "text/") {
		return formatText
	}
	return formatUnsupported
}

// extractText reads the attachment as text, capped at maxChars characters.
//
// The second return value reports a truncated document, so the caller can tell
// the LLM it is not reading the whole thing.
func extractText(att attachment, maxBytes, maxChars int) (string, bool, error) {
	if maxBytes > 0 && len(att.Data) > maxBytes {
		return "", false, fmt.Errorf("%w: %d bytes", errTooLarge, len(att.Data))
	}

	format := detectFormat(att)
	if format == formatUnsupported {
		return "", false, fmt.Errorf("unsupported format (media type %q, name %q)", att.MediaType, att.Name)
	}

	var (
		text string
		err  error
	)
	if format == formatText {
		text = string(att.Data)
		if !utf8.ValidString(text) {
			return "", false, fmt.Errorf("not valid UTF-8 text")
		}
		if len(text) > maxChars {
			text = truncateChars(text, maxChars)
		}
	} else {
		text, err = walkDocument(format, att, maxChars)
		if err != nil {
			return "", false, err
		}
	}

	if strings.TrimSpace(text) == "" {
		return "", false, errNoText
	}
	return text, utf8.RuneCountInString(text) >= maxChars, nil
}

// walkDocument writes the attachment to a temporary file — every go-anon
// walker reads from a path — and accumulates its text segments.
func walkDocument(format docFormat, att attachment, maxChars int) (string, error) {
	path, cleanup, err := writeTempFile(att, string(format))
	if err != nil {
		return "", err
	}
	defer cleanup()

	var walker docprocessor.Walker
	switch format {
	case formatPDF:
		walker, err = pdf.NewWalkerFromFile(path)
	case formatDOCX:
		walker, err = docx.NewWalkerFromFile(path)
	case formatODT:
		walker, err = odt.NewWalkerFromFile(path)
	case formatCSV:
		walker, err = csv.NewWalkerFromFile(path)
	default:
		return "", fmt.Errorf("no walker for format %q", format)
	}
	if err != nil {
		return "", fmt.Errorf("open %s document: %w", format, err)
	}

	// SampleText walks read-only and stops once it has enough: a walker never
	// gets to write back, and an oversized document cannot exhaust memory.
	text, err := docprocessor.SampleText(walker, maxChars)
	if err != nil {
		return "", fmt.Errorf("read %s document: %w", format, err)
	}
	return text, nil
}

// writeTempFile spills the attachment bytes to disk for the walkers to open.
// The returned cleanup removes the file: it holds the very data the plugin
// exists to keep from spreading.
func writeTempFile(att attachment, suffix string) (string, func(), error) {
	f, err := os.CreateTemp("", "pseudonymizer-*."+suffix)
	if err != nil {
		return "", nil, fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()
	cleanup := func() {
		os.Remove(path)
	}

	if _, err := f.Write(att.Data); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close temp file: %w", err)
	}
	return path, cleanup, nil
}

// truncateChars cuts text to at most n characters, on a rune boundary.
func truncateChars(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	count := 0
	for i := range text {
		if count == n {
			return text[:i]
		}
		count++
	}
	return text
}
