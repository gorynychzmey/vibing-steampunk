package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/oisee/vibing-steampunk/pkg/saprfc"
)

// handleImportTransport imports released requests into the system this server
// is connected to, as STMS_IMPORT does there:
// SAP(action="system", params={"type": "import_transport", "transport": "TR-A", "client": "100"}).
// It goes over classic RFC to CTS_API_IMPORT_CHANGE_REQUEST in that system; the
// target is always this server's own system (see saprfc.ImportRequests).
func (s *Server) handleImportTransport(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	requests := transportList(args["transport"])
	if len(requests) == 0 {
		requests = transportList(args["transports"])
	}
	safety := s.adtClient.Safety()
	if len(requests) == 0 && safety.AllowTransportImport {
		return newToolResultError("transport (one request, a comma-separated list or an array) is required"), nil
	}
	// The import's own opt-in, then read-only, transport read-only and the
	// allowed transports: an import changes the system, so it fails closed.
	if err := safety.CheckTransportImport(requests); err != nil {
		return newToolResultError(err.Error()), nil
	}
	client := getStringParam(args, "client")
	if client == "" {
		client = s.config.Client
	}

	timeout := saprfc.ImportTimeout
	if secs := intParam(args, "timeout", 0); secs > 0 {
		timeout = time.Duration(secs) * time.Second
	}
	// The target is this server's own system, and only that: a per-call
	// gateway, system number, port or user would send the import to a system
	// the switch was never enabled for.
	for _, key := range importDestinationOverrides {
		if _, set := args[key]; set {
			return newToolResultError(fmt.Sprintf("import_transport does not take %q: the import always goes to this "+
				"server's own system (set rfc_host/rfc_sysnr/rfc_port/rfc_user in its .vsp.json entry instead)", key)), nil
		}
	}
	// The import gets a connection of its own: the shared one gives up after
	// the library's 30 s, and a production import runs far longer than that.
	dest, err := s.rfcDestination(args)
	if err != nil {
		return newToolResultError(fmt.Sprintf("importing needs classic RFC to this system: %v", err)), nil
	}
	run := func(ctx context.Context) (*saprfc.ImportResult, error) {
		c, oerr := saprfc.OpenWithTimeout(ctx, dest, timeout)
		if oerr != nil {
			return nil, fmt.Errorf("RFC logon to %s:%d failed: %w", dest.Host, dest.Port, oerr)
		}
		defer func() { _ = c.Close(context.Background()) }()
		return saprfc.ImportRequests(ctx, c, requests, client)
	}

	if async, _ := getBoolParam(args, "async"); async {
		return newToolResultJSON(s.startImport(run, requests, client, timeout)), nil
	}
	res, err := run(ctx)
	if err != nil {
		// The partial result stays, but the call is an error: a non-000 TMS
		// code must not read as a successful tool call.
		out := newToolResultJSON(map[string]any{"error": err.Error(), "result": res})
		out.IsError = true
		return out, nil
	}
	return newToolResultJSON(res), nil
}

// importDestinationOverrides are the rfcDestination parameters that would
// point a call at another system; the import refuses them.
var importDestinationOverrides = []string{"host", "sysnr", "port", "user"}

// startImport runs an import in the background and returns at once, with the
// task to follow it by. The import outlives the tool call; it is bounded by
// its own timeout, not by the request that started it.
func (s *Server) startImport(run func(context.Context) (*saprfc.ImportResult, error), requests []string, client string, timeout time.Duration) map[string]any {
	started := time.Now()
	s.asyncTasksMu.Lock()
	s.asyncTaskID++
	taskID := fmt.Sprintf("import_%d_%d", started.Unix(), s.asyncTaskID)
	task := &AsyncTask{ID: taskID, Type: "import", Status: "running", StartedAt: started}
	s.asyncTasks[taskID] = task
	s.asyncTasksMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Minute)
		defer cancel()
		res, err := run(ctx)
		s.asyncTasksMu.Lock()
		defer s.asyncTasksMu.Unlock()
		now := time.Now()
		task.EndedAt = &now
		task.Result = res
		if err != nil {
			task.Status = "error"
			task.Error = err.Error()
			return
		}
		task.Status = "completed"
	}()

	return map[string]any{
		"task_id":  taskID,
		"status":   "started",
		"requests": requests,
		"client":   client,
		"since":    started.Format("20060102150405"),
		"follow":   fmt.Sprintf(`SAP(action="debug", target="GET_ASYNC_RESULT", params={"task_id": %q, "wait_seconds": 600})`, taskID),
		"log": fmt.Sprintf(`SAP(action="system", params={"type": "import_status", "transport": %q, "since": %q})`,
			strings.Join(requests, ","), started.Format("20060102150405")),
	}
}

// handleImportStatus reports what TPALOG holds for requests -- the tp steps of
// their imports into this system, and the worst return code:
// SAP(action="system", params={"type": "import_status", "transport": "TR-A", "since": "20260101120000"}).
// It needs nothing from the call that started the import, so it works after a
// restart too.
func (s *Server) handleImportStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	requests := transportList(args["transport"])
	if len(requests) == 0 {
		requests = transportList(args["transports"])
	}
	if len(requests) == 0 {
		return newToolResultError("transport (one request, a comma-separated list or an array) is required"), nil
	}
	c, release, err := s.rfcClientFor(ctx, args)
	if err != nil {
		return newToolResultError(fmt.Sprintf("reading TPALOG needs classic RFC to this system: %v", err)), nil
	}
	defer release()
	logs, err := saprfc.ReadImportLog(ctx, c, requests, getStringParam(args, "since"))
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	return newToolResultJSON(logs), nil
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
