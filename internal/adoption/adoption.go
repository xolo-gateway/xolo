// Package adoption defines the inventory a control plane adopts an instance
// from: every projection of the common contract on one snapshot, with the
// event cursor to resume from. It never restores data nor changes an ID.
//
// The format is NDJSON, written and verified as a stream:
//
//	{"version":"xolo-adoption/1","contract":…,"source":…,"c0":…,"families":[…]}
//	{"family":…,"key":{…},"representation":{…},"etag":…}   (one per record)
//	{"count":…,"complete":true,"sha256":…}
//
// The SHA-256 covers the exact bytes of every line before the last one,
// newlines included. It detects corruption and truncation, not a malicious
// replacement: the transport and the file permissions establish provenance.
package adoption

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// Version identifies the format.
const Version = "xolo-adoption/1"

// Families lists the exported families, parents before children: records
// follow this order.
var Families = append(append([]string{}, model.CommonFamilies...), model.BusinessFamilies...)

// Header is the first line of an export.
type Header struct {
	Version  string   `json:"version"`
	Contract string   `json:"contract"`
	Source   string   `json:"source"`
	Cursor   string   `json:"c0"`
	Families []string `json:"families"`
}

// Record is one projection.
type Record struct {
	Family         string          `json:"family"`
	Key            model.CommonKey `json:"key"`
	Representation json.RawMessage `json:"representation"`
	ETag           string          `json:"etag"`
}

// Trailer is the last line of a complete export.
type Trailer struct {
	Count    int    `json:"count"`
	Complete bool   `json:"complete"`
	SHA256   string `json:"sha256"`
}

// Export writes the inventory of reader to w. On failure the output lacks
// its trailer, so Verify rejects it.
func Export(ctx context.Context, reader port.InventoryReader, w io.Writer) error {
	out := bufio.NewWriter(w)
	digest := sha256.New()
	count := 0
	err := reader.ReadInventory(ctx,
		func(source, cursor string) error {
			return writeLine(out, digest, Header{Version: Version, Contract: model.CommonContractVersion, Source: source, Cursor: cursor, Families: Families})
		},
		func(family string, item model.CommonItem) error {
			count++
			return writeLine(out, digest, Record{Family: family, Key: item.Key, Representation: item.Representation, ETag: item.ETag})
		},
	)
	if err != nil {
		return err
	}
	if count == 0 {
		// The default tenant always exists: an empty inventory is a fault.
		return errors.New("empty inventory")
	}
	if err := writeLine(out, nil, Trailer{Count: count, Complete: true, SHA256: hex.EncodeToString(digest.Sum(nil))}); err != nil {
		return err
	}
	return errors.WithStack(out.Flush())
}

func writeLine(w io.Writer, digest hash.Hash, value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return errors.WithStack(err)
	}
	line = append(line, '\n')
	if digest != nil {
		digest.Write(line)
	}
	_, err = w.Write(line)
	return errors.WithStack(err)
}
