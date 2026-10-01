// handlers_rfc.go adds classic RFC to the universal SAP tool: action="rfc" with
// an op in params. RFC is a second protocol to the same system (gateway instead
// of HTTP/ADT), served by the SDK-free open-rfc-go client. It is one action, not
// a family of tools, so the MCP tool space stays a single SAP tool.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	openrfc "github.com/oisee/open-rfc-go/rfc"

	"github.com/oisee/vibing-steampunk/pkg/adt"
	"github.com/oisee/vibing-steampunk/pkg/config"
	"github.com/oisee/vibing-steampunk/pkg/saprfc"
)

// routeRFCAction handles SAP(action="rfc", …).
//
//	SAP(action="rfc", params={"op":"info"})                       — RFC_SYSTEM_INFO
//	SAP(action="rfc", params={"op":"ping"})                       — RFC_PING
//	SAP(action="rfc", params={"op":"probe"})                      — system fingerprint:
//	    release, components, helper presence, and what this user may call
//	SAP(action="rfc", target="BAPI_USER_*", params={"op":"search"})
//	SAP(action="rfc", target="STFC_CONNECTION")                   — describe (default)
//	SAP(action="rfc", target="Z_DOUBLE", params={"op":"call","args":{"N":21}})
//	SAP(action="rfc", target="T000", params={"op":"read_table","fields":["MANDT"],"top":5})
//	SAP(action="rfc", target="ZREPORT", params={"op":"run","variant":"V1","params":{"P_WERKS":"1000"}})
//	                                                                — background job, spool, job log
//	SAP(action="rfc", target="VSP_ZREPORT", params={"op":"job","job_count":"12345678"})
//
// Destination overrides: params host / sysnr / port / user.
func (s *Server) routeRFCAction(ctx context.Context, action, objectType, objectName string, params map[string]any) (result *mcp.CallToolResult, handled bool, rfcErr error) {
	if action != "rfc" {
		return nil, false, nil
	}
	// The target is a plain name (FM or table), so parseTarget puts it in objectType.
	name := strings.TrimSpace(objectName)
	if name == "" {
		name = strings.TrimSpace(objectType)
	}
	op := strings.ToLower(getStringParam(params, "op"))
	if op == "" {
		switch {
		case name == "":
			op = "info"
		case params["args"] != nil:
			op = "call"
		default:
			op = "describe"
		}
	}
	// With an explicit op the name may also come as a named parameter, which is
	// how the help has always spelled read_table and search.
	if name == "" {
		if key := rfcNameKey(op); key != "" {
			name = strings.TrimSpace(getStringParam(params, key))
		}
	}

	// run starts a background job that does whatever the report does, so it
	// passes the safety configuration first, as ExecuteABAP does (OpWorkflow):
	// --read-only and --disallowed-ops W refuse it before any connection.
	if op == "run" {
		if err := s.adtClient.Safety().CheckOperation(adt.OpWorkflow, "RunReport"); err != nil {
			return nil, true, err
		}
	}

	c, release, err := s.rfcClientFor(ctx, params)
	if err != nil {
		return nil, true, err
	}
	defer release()

	// A dead shared connection must not poison every later call.
	defer func() {
		if rfcErr != nil && (errors.Is(rfcErr, openrfc.ErrTransport) || errors.Is(rfcErr, openrfc.ErrClosed)) {
			s.dropSharedRFC(ctx)
		}
	}()

	switch op {
	case "info":
		r, err := c.Call(ctx, "RFC_SYSTEM_INFO", nil)
		if err != nil {
			return nil, true, err
		}
		return rfcResult(r.Get("RFCSI_EXPORT"))
	case "probe":
		dest, derr := s.rfcDestination(params)
		if derr != nil {
			return nil, true, derr
		}
		probe, perr := saprfc.RunProbe(ctx, c, dest)
		if perr != nil {
			return nil, true, perr
		}
		return rfcResult(probe)
	case "ping":
		if _, err := c.Call(ctx, "RFC_PING", nil); err != nil {
			return nil, true, err
		}
		return mcp.NewToolResultText("ok"), true, nil
	case "describe":
		if name == "" {
			return nil, true, fmt.Errorf("describe needs a function module in target")
		}
		tool, err := c.DescribeTool(ctx, strings.ToUpper(name))
		if err != nil {
			return nil, true, err
		}
		return rfcResult(tool)
	case "call":
		if name == "" {
			return nil, true, fmt.Errorf("call needs a function module in target")
		}
		args, _ := params["args"].(map[string]any)
		r, err := c.Call(ctx, strings.ToUpper(name), openrfc.Params(args))
		if err != nil {
			return nil, true, err
		}
		return rfcResult(r)
	case "search":
		like := strings.ReplaceAll(strings.ToUpper(name), "*", "%")
		if like == "" {
			return nil, true, fmt.Errorf("search needs a name mask in target")
		}
		if !strings.Contains(like, "%") {
			like = "%" + like + "%"
		}
		where := "FUNCNAME LIKE '" + like + "'"
		if all, ok := getBoolParam(params, "all"); !ok || !all {
			// 'R' and 'X' are both remote-enabled; 'X' additionally marks the
			// interface basXML-capable, which SAP sets on every FM with
			// deep/nested parameters. See pkg/saprfc/adt.go.
			where += " AND FMODE IN ( 'R', 'X' )"
		}
		rows, err := saprfc.ReadTable(ctx, c, "TFDIR", where, []string{"FUNCNAME", "PNAME"}, intParam(params, "top", 100))
		if err != nil {
			return nil, true, err
		}
		return rfcResult(rows)
	case "read_table", "read-table", "table":
		if name == "" {
			return nil, true, fmt.Errorf("read_table needs a table name in target")
		}
		var fields []string
		if raw, ok := params["fields"].([]any); ok {
			for _, f := range raw {
				fields = append(fields, strings.ToUpper(fmt.Sprint(f)))
			}
		}
		rows, err := saprfc.ReadTable(ctx, c, strings.ToUpper(name), getStringParam(params, "where"), fields, intParam(params, "top", 0))
		if err != nil {
			return nil, true, err
		}
		return rfcResult(rows)
	case "run":
		if name == "" {
			return nil, true, fmt.Errorf("run needs a report in target")
		}
		sel, err := reportParams(params["params"])
		if err != nil {
			return nil, true, err
		}
		wait := time.Duration(intParam(params, "wait", 60)) * time.Second
		if wait > maxReportWait {
			wait = maxReportWait
		}
		run, err := saprfc.RunReportWith(ctx, c, saprfc.ReportRequest{
			Report: name, JobName: getStringParam(params, "job_name"),
			Variant: getStringParam(params, "variant"), Params: sel, Wait: wait,
		})
		if err != nil {
			return nil, true, err
		}
		readJobOutput(ctx, c, run, params)
		return rfcResult(run)
	case "job":
		count := strings.TrimSpace(getStringParam(params, "job_count"))
		if name == "" || count == "" {
			return nil, true, fmt.Errorf("job needs the job name in target and job_count")
		}
		status, text, err := saprfc.JobStatus(ctx, c, strings.ToUpper(name), count)
		if err != nil {
			return nil, true, err
		}
		if status == "" {
			return nil, true, fmt.Errorf("no job %s / %s", strings.ToUpper(name), count)
		}
		run := &saprfc.JobRun{JobName: strings.ToUpper(name), JobCount: count, Status: status, StatusFor: text}
		readJobOutput(ctx, c, run, params)
		return rfcResult(run)
	}
	return nil, true, fmt.Errorf("unknown rfc op %q (info, ping, probe, describe, call, search, read_table, run, job)", op)
}

// rfcClientFor returns a client for this call and a release function. Calls
// without a destination override share one connection pool for the life of the
// server — an RFC logon per tool call is slow and needless — while a call that
// overrides host/sysnr/port/user gets its own client, closed on release.
func (s *Server) rfcClientFor(ctx context.Context, params map[string]any) (*openrfc.Client, func(), error) {
	overridden := getStringParam(params, "host") != "" || getStringParam(params, "sysnr") != "" ||
		getStringParam(params, "user") != "" || intParam(params, "port", 0) != 0
	if overridden {
		c, err := s.dialRFC(ctx, params)
		if err != nil {
			return nil, nil, err
		}
		return c, func() { _ = c.Close(ctx) }, nil
	}

	s.rfcMu.Lock()
	defer s.rfcMu.Unlock()
	if s.rfcShared == nil {
		c, err := s.dialRFC(ctx, params)
		if err != nil {
			return nil, nil, err
		}
		s.rfcShared = c
		s.startRFCKeepAlive()
	}
	s.rfcLastUsed = time.Now()
	return s.rfcShared, func() {}, nil
}

// rfcKeepAliveInterval is how often an idle shared connection is pinged. SAP
// gateways and work processes drop conversations that go quiet, and an MCP
// session can sit idle for a long time between a user's questions. One minute is
// deliberately conservative: an RFC_PING is a few hundred bytes, far cheaper than
// the logon a dropped connection would cost.
const rfcKeepAliveInterval = time.Minute

// startRFCKeepAlive pings the shared connection while it is idle, so the next
// tool call does not pay for a fresh logon (or fail outright). It exits once the
// shared client is gone. Must be called with rfcMu held.
func (s *Server) startRFCKeepAlive() {
	go func() {
		ticker := time.NewTicker(rfcKeepAliveInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.rfcMu.Lock()
			c := s.rfcShared
			idle := time.Since(s.rfcLastUsed)
			s.rfcMu.Unlock()
			if c == nil {
				return // the client was dropped; nothing to keep alive
			}
			if idle < rfcKeepAliveInterval {
				continue // a real call kept it warm
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := c.Call(ctx, "RFC_PING", nil)
			cancel()
			if err != nil {
				// The connection is gone: forget it so the next call redials.
				s.dropSharedRFC(context.Background())
				return
			}
		}
	}()
}

// dropSharedRFC forgets a shared client whose connection died, so the next call
// logs on again instead of failing forever.
func (s *Server) dropSharedRFC(ctx context.Context) {
	s.rfcMu.Lock()
	defer s.rfcMu.Unlock()
	if s.rfcShared != nil {
		_ = s.rfcShared.Close(ctx)
		s.rfcShared = nil
	}
}

// dialRFC resolves the destination for this server's system, honouring per-call
// overrides and the RFC settings of the default .vsp.json system.
func (s *Server) dialRFC(ctx context.Context, params map[string]any) (*openrfc.Client, error) {
	dest, err := s.rfcDestination(params)
	if err != nil {
		return nil, err
	}
	c, err := saprfc.Open(ctx, dest)
	if err != nil {
		return nil, fmt.Errorf("RFC logon to %s:%d failed: %w", dest.Host, dest.Port, err)
	}
	return c, nil
}

// rfcDestination resolves where an RFC call goes: this server's system, the RFC
// settings of the default .vsp.json system, and any per-call override.
func (s *Server) rfcDestination(params map[string]any) (saprfc.Params, error) {
	in := saprfc.Input{
		URL:      s.config.BaseURL,
		User:     s.config.Username,
		Password: s.config.Password,
		Client:   s.config.Client,
		Language: s.config.Language,
		RFCUser:  os.Getenv("SAP_USER"),
	}
	if pwd := os.Getenv("SAP_PASSWORD"); pwd != "" {
		in.RFCPassword = pwd
	}
	// Per-system RFC settings from the default .vsp.json system, when present.
	if cfg, _, err := config.LoadSystems(); err == nil && cfg != nil && cfg.Default != "" {
		if sys, err := cfg.GetSystem(cfg.Default); err == nil {
			in.RFCHost, in.RFCSysnr, in.RFCPort = sys.RFCHost, sys.RFCSysnr, sys.RFCPort
			if sys.RFCUser != "" {
				in.RFCUser = sys.RFCUser
			}
			if sys.RFCPassword != "" {
				in.RFCPassword = sys.RFCPassword
			}
		}
	}
	in.HostFlag = getStringParam(params, "host")
	in.SysnrFlag = getStringParam(params, "sysnr")
	in.UserFlag = getStringParam(params, "user")
	in.PortFlag = intParam(params, "port", 0)

	return saprfc.Resolve(in)
}

func rfcResult(v any) (*mcp.CallToolResult, bool, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, true, err
	}
	return mcp.NewToolResultText(string(b)), true, nil
}

func intParam(params map[string]any, key string, def int) int {
	if v, ok := getFloatParam(params, key); ok {
		return int(v)
	}
	return def
}

// maxReportWait caps how long a run waits for its job. A report that runs
// longer is left running; op "job" picks up its outcome later.
const maxReportWait = 5 * time.Minute

// readJobOutput adds what a job left behind to run: its log, and once it has
// finished, its spool list. Neither is worth failing the call over -- the job
// ran either way -- so a read that fails is reported in the result instead.
func readJobOutput(ctx context.Context, c *openrfc.Client, run *saprfc.JobRun, params map[string]any) {
	ended := run.Status == "F" || run.Status == "A"
	if want, ok := getBoolParam(params, "spool"); (!ok || want) && run.Status == "F" {
		if spool, err := saprfc.ReadSpool(ctx, c, run.JobName, run.JobCount); err != nil {
			run.Spool = "spool unavailable: " + err.Error()
		} else {
			run.Spool, run.SpoolTruncated = truncateAtLine(spool, intParam(params, "spool_max_bytes", defaultSpoolMaxBytes))
		}
	}
	if want, ok := getBoolParam(params, "joblog"); (!ok || want) && ended {
		if log, err := saprfc.ReadJobLog(ctx, c, run.JobName, run.JobCount); err == nil {
			run.JobLog = log
		} else {
			run.JobLog = []saprfc.JobLogEntry{{Type: "E", Text: "job log unavailable: " + err.Error()}}
		}
	}
}

// reportParams reads a report's selection values. Either an object -- a scalar
// value is a parameter, a list of scalars a select-option of single values --
// or a list of rows as RSPARAMS spells them (name, kind, sign, option, low,
// high).
func reportParams(v any) ([]saprfc.ReportParam, error) {
	var out []saprfc.ReportParam
	switch sel := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		names := make([]string, 0, len(sel))
		for k := range sel {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			switch val := sel[name].(type) {
			case []any:
				for _, one := range val {
					out = append(out, saprfc.ReportParam{Name: name, Kind: "S", Low: scalar(one)})
				}
			case map[string]any:
				return nil, fmt.Errorf("selection %s: use the list form for ranges, e.g. [{\"name\": %q, \"kind\": \"S\", \"option\": \"BT\", \"low\": …, \"high\": …}]", name, name)
			default:
				out = append(out, saprfc.ReportParam{Name: name, Kind: "P", Low: scalar(val)})
			}
		}
	case []any:
		for i, raw := range sel {
			row, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("selection row %d is not an object", i+1)
			}
			get := func(k string) string {
				for key, val := range row {
					if strings.EqualFold(key, k) {
						return scalar(val)
					}
				}
				return ""
			}
			p := saprfc.ReportParam{Name: get("name"), Kind: get("kind"), Sign: get("sign"),
				Option: get("option"), Low: get("low"), High: get("high")}
			if p.Name == "" {
				p.Name = get("selname")
			}
			if p.Name == "" {
				return nil, fmt.Errorf("selection row %d has no name", i+1)
			}
			if p.Kind == "" && (p.High != "" || p.Option != "" || p.Sign != "") {
				p.Kind = "S"
			}
			out = append(out, p)
		}
	default:
		return nil, fmt.Errorf("params: an object of selection values or a list of RSPARAMS rows")
	}
	return out, nil
}

// scalar renders a JSON value as the text a selection screen takes. JSON
// numbers arrive as float64; an integer is written without a fraction.
func scalar(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case float64:
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	case bool:
		if n {
			return "X"
		}
		return ""
	}
	return fmt.Sprint(v)
}

// rfcNameKey is the named parameter that may carry an op's object name when
// the target leaves it out. Only the one the op takes: a job_name given to
// "run" is not the report to run.
func rfcNameKey(op string) string {
	switch op {
	case "run":
		return "report"
	case "job":
		return "job_name"
	case "read_table", "read-table", "table":
		return "table"
	case "search":
		return "pattern"
	case "describe", "call":
		return "function"
	}
	return ""
}

// defaultSpoolMaxBytes caps the spool list a run returns: a report without
// limits can print far more than one MCP message should carry.
const defaultSpoolMaxBytes = 256 * 1024

// truncateAtLine cuts s to at most max bytes at a line end, and says whether
// it cut. A max of zero or less keeps everything.
func truncateAtLine(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i+1]
	}
	return cut, true
}
