package mcpclient

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/errors"
)

// ResourceInfo is a lightweight description of an MCP resource, suitable for
// crossing a gRPC boundary: the plugin resolves a configured resource by URI or
// by name and hands its content to the model.
type ResourceInfo struct {
	URI         string
	Name        string
	Description string
	MIMEType    string
}

// ListResourceInfos lists the resources the server exposes. A server without
// the resources capability answers with none, which is not an error.
func ListResourceInfos(ctx context.Context, session *mcp.ClientSession) ([]ResourceInfo, error) {
	if !supportsResources(session) {
		return nil, nil
	}
	var infos []ResourceInfo
	params := &mcp.ListResourcesParams{}
	for {
		result, err := session.ListResources(ctx, params)
		if err != nil {
			return nil, errors.Wrap(err, "list MCP resources")
		}
		for _, r := range result.Resources {
			infos = append(infos, ResourceInfo{
				URI:         r.URI,
				Name:        r.Name,
				Description: r.Description,
				MIMEType:    r.MIMEType,
			})
		}
		if result.NextCursor == "" {
			return infos, nil
		}
		params.Cursor = result.NextCursor
	}
}

// FindResource resolves a configured reference to a resource the server
// exposes. The reference is matched against the URI first, then against the
// name, so a configuration can say "readme" as well as "file:///readme.md".
func FindResource(ctx context.Context, session *mcp.ClientSession, ref string) (*ResourceInfo, error) {
	infos, err := ListResourceInfos(ctx, session)
	if err != nil {
		return nil, err
	}
	for i, r := range infos {
		if r.URI == ref {
			return &infos[i], nil
		}
	}
	for i, r := range infos {
		if r.Name == ref {
			return &infos[i], nil
		}
	}
	return nil, errors.Errorf("no MCP resource named %q", ref)
}

// ReadResourceText reads a resource and returns its textual content, which is
// what a model can be given. Binary contents are reported by their URI and MIME
// type rather than embedded: a request body carries text, and a base64 blob of
// an arbitrary size is not something a model can use.
func ReadResourceText(ctx context.Context, session *mcp.ClientSession, uri string) (string, error) {
	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return "", errors.Wrapf(err, "read MCP resource %q", uri)
	}

	var parts []string
	for _, c := range result.Contents {
		switch {
		case c.Text != "":
			parts = append(parts, c.Text)
		case len(c.Blob) > 0:
			parts = append(parts, "["+contentType(c.MIMEType)+" content of "+c.URI+", not shown]")
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// supportsResources reports whether the server advertised the resources
// capability. A server that did not is asked nothing, rather than failed on.
func supportsResources(session *mcp.ClientSession) bool {
	init := session.InitializeResult()
	if init == nil || init.Capabilities == nil {
		return false
	}
	return init.Capabilities.Resources != nil
}

func contentType(mimeType string) string {
	if mimeType == "" {
		return "binary"
	}
	return mimeType
}
