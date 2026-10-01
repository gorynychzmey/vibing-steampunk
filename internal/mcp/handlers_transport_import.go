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
// SAP(action="system", params={"type": "import_transport", "transport": "TR-A"}).
// The client is this server's own; a "client" naming another one is refused.
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
	// The import goes into this server's own client. The opt-in and every
	// switch above belong to the system entry this server was configured
	// with -- URL and client -- so a per-call client is taken only when it
	// names that client.
	client, err := importClient(getStringParam(args, "client"), s.config.Client)
	if err != nil {
		return newToolResultError(err.Error()), nil
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
		// After a lost call it logs on again to read TPALOG.
		return saprfc.ImportRequestsRecheck(ctx, c, dest, requests, client)
	}

	if async, _ := getBoolParam(args, "async"); async {
		return newToolResultJSON(s.startImport(run, requests, client, timeout)), nil
	}
	res, err := run(ctx)
	if err != nil {
		return importErrorResult(requests, res, err), nil
	}
	return newToolResultJSON(res), nil
}

// importErrorResult is the tool result of an import that did not succeed.
// The partial result stays, but the call is an error: a non-000 TMS code
// must not read as a successful tool call. An import submitted and then lost
// is marked unknown, with how to find out what happened, so it is not taken
// for a failure to retry.
func importErrorResult(requests []string, res *saprfc.ImportResult, err error) *mcp.CallToolResult {
	body := map[string]any{"error": err.Error(), "result": res}
	if res != nil && res.Outcome == saprfc.OutcomeUnknown {
		body["outcome"] = saprfc.OutcomeUnknown
		body["advice"] = res.Advice
		body["check"] = importStatusCall(requests, res.Submitted)
	}
	out := newToolResultJSON(body)
	out.IsError = true
	return out
}

// importClient is the client an import goes into: this server's own. A
// per-call client is accepted only when it names the same client; any other
// would import into a client the opt-in and the safety settings were never
// configured for.
func importClient(perCall, own string) (string, error) {
	perCall, own = strings.TrimSpace(perCall), strings.TrimSpace(own)
	if perCall == "" || perCall == own {
		return own, nil
	}
	if own == "" {
		return "", fmt.Errorf("import_transport client %q is blocked: this server has no client of its own "+
			"configured, and an import goes only into the client its system entry names", perCall)
	}
	return "", fmt.Errorf("import_transport client %q is blocked: it differs from this server's own client %s, "+
		"and the import's opt-in and safety settings belong to that client (configure the other client as "+
		"its own system in .vsp.json and use a server connected to it)", perCall, own)
}

// importStatusCall is the call that follows an import in TPALOG.
func importStatusCall(requests []string, since string) string {
	return fmt.Sprintf(`SAP(action="system", params={"type": "import_status", "transport": %q, "since": %q})`,
		strings.Join(requests, ","), since)
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
			if res != nil && res.Outcome == saprfc.OutcomeUnknown {
				// Not an error to retry: the import may be running.
				task.Status = saprfc.OutcomeUnknown
			}
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
		"log":      importStatusCall(requests, started.Format("20060102150405")),
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

// SameSystem says whether two URL/client pairs name one system, by the rule
// the server uses to find its own .vsp.json entry (an omitted client is the
// default client).
func SameSystem(urlA, clientA, urlB, clientB string) bool {
	return sameSystem(urlA, clientA, urlB, clientB)
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
