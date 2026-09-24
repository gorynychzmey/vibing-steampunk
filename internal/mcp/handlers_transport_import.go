package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/oisee/vibing-steampunk/pkg/saprfc"
)

// handleImportTransport imports released requests into the system this server
// is connected to, as STMS_IMPORT does there:
// SAP(action="system", params={"type": "import_transport", "transport": "TR-A", "client": "100"}).
// It goes over classic RFC to CTS_API_IMPORT_CHANGE_REQUEST in that system; the
// target is always this server's own system (see saprfc.ImportRequests).
func (s *Server) handleImportTransport(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.adtClient.Safety().AllowTransportImport {
		return newToolResultError("importing requests is off for this system; enable it with allow_transport_import " +
			"in .vsp.json, --allow-transport-import or SAP_ALLOW_TRANSPORT_IMPORT=true"), nil
	}
	args := request.GetArguments()
	requests := transportList(args["transport"])
	if len(requests) == 0 {
		requests = transportList(args["transports"])
	}
	if len(requests) == 0 {
		return newToolResultError("transport (one request, a comma-separated list or an array) is required"), nil
	}
	client := getStringParam(args, "client")
	if client == "" {
		client = s.config.Client
	}

	c, release, err := s.rfcClientFor(ctx, args)
	if err != nil {
		return newToolResultError(fmt.Sprintf("importing needs classic RFC to this system: %v", err)), nil
	}
	defer release()

	res, err := saprfc.ImportRequests(ctx, c, requests, client)
	if err != nil {
		return newToolResultJSON(map[string]any{"error": err.Error(), "result": res}), nil
	}
	return newToolResultJSON(res), nil
}

// transportList reads one request, a comma-separated list, or an array.
func transportList(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		for _, p := range strings.Split(t, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	case []any:
		for _, p := range t {
			if str, ok := p.(string); ok && strings.TrimSpace(str) != "" {
				out = append(out, strings.TrimSpace(str))
			}
		}
	}
	return out
}
