package saprfc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oisee/open-rfc-go/rfc"
)

// Running a report is the classic thing ADT cannot do: the APC WebSocket forbids
// SUBMIT (APC_ILLEGAL_STATEMENT), which is why issues about running reports from
// vsp were closed as an architectural limit. Over RFC there is no such ban — the
// XBP BAPIs schedule the report as a background job, TBTCO carries its status, and
// XBP returns the spool it produced.

// ReportParam is one selection-screen parameter or select-option line.
type ReportParam struct {
	Name   string `json:"name"`             // SELNAME, e.g. "P_MATNR" or "S_WERKS"
	Kind   string `json:"kind,omitempty"`   // "P" parameter, "S" select-option (default P)
	Sign   string `json:"sign,omitempty"`   // "I" include (default) or "E" exclude
	Option string `json:"option,omitempty"` // "EQ" (default), "BT", "CP", …
	Low    string `json:"low"`
	High   string `json:"high,omitempty"`
}

// JobRun is the outcome of scheduling a report.
type JobRun struct {
	Report    string        `json:"report"`
	JobName   string        `json:"job_name"`
	JobCount  string        `json:"job_count"`
	Status    string        `json:"status,omitempty"`      // TBTCO STATUS once known
	StatusFor string        `json:"status_text,omitempty"` // human reading of that letter
	Spool     string        `json:"spool,omitempty"`       // spool list, when requested and available
	JobLog    []JobLogEntry `json:"job_log,omitempty"`     // job log, when requested
}

// jobStatusText turns a TBTCO status letter into words.
func jobStatusText(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P":
		return "scheduled"
	case "S":
		return "released"
	case "R":
		return "running"
	case "F":
		return "finished"
	case "A":
		return "cancelled"
	case "Y":
		return "ready"
	case "":
		return ""
	}
	return "unknown"
}

// RunReport runs a report as a background job through the XBP interface.
//
// SUBST_START_REPORT_IN_BATCH looks like the obvious call, but it picks a batch
// server itself and fails with BATCH_SCHEDULING_FAILED (XM262) on systems where
// that selection does not resolve — a containerised A4H, for one, even with
// SAP_ALL and free batch work processes. The XBP BAPIs are the supported external
// scheduler interface: they take the target server explicitly, so they work where
// SUBST does not, and they carry a BAPIRET2 that says what went wrong.
func RunReport(ctx context.Context, c *rfc.Client, report, jobName string, params []ReportParam, wait time.Duration) (*JobRun, error) {
	return RunReportWith(ctx, c, ReportRequest{Report: report, JobName: jobName, Params: params, Wait: wait})
}

// ReportRequest is one report run: the report, the job it runs in, and what it
// runs with -- a saved variant, selection values, or both (the values then
// override the variant's).
type ReportRequest struct {
	Report  string
	JobName string // defaults to VSP_<REPORT>
	Variant string
	Params  []ReportParam
	Wait    time.Duration // how long to wait for the job to end; zero returns at once
}

// RunReportWith runs a report as a background job through the XBP interface.
func RunReportWith(ctx context.Context, c *rfc.Client, r ReportRequest) (*JobRun, error) {
	report, jobName, params, wait := strings.ToUpper(strings.TrimSpace(r.Report)), r.JobName, r.Params, r.Wait
	if report == "" {
		return nil, fmt.Errorf("a report name is required")
	}
	if jobName == "" {
		jobName = "VSP_" + report
	}
	if len(jobName) > 32 {
		jobName = jobName[:32]
	}

	if err := xmiLogon(ctx, c); err != nil {
		return nil, err
	}
	defer func() { _, _ = c.Call(ctx, "BAPI_XMI_LOGOFF", rfc.Params{"INTERFACE": "XBP"}) }()

	run := &JobRun{Report: report, JobName: jobName}

	opened, err := c.Call(ctx, "BAPI_XBP_JOB_OPEN", rfc.Params{
		"JOBNAME": jobName, "EXTERNAL_USER_NAME": xbpUser,
	})
	if err != nil {
		return nil, fmt.Errorf("BAPI_XBP_JOB_OPEN: %w", err)
	}
	if err := bapiError("BAPI_XBP_JOB_OPEN", opened.Get("RETURN")); err != nil {
		return nil, err
	}
	run.JobCount = strings.TrimSpace(fmt.Sprint(opened.Get("JOBCOUNT")))

	// A job that was opened but never started stays in SM37 as "scheduled"
	// forever. If anything below fails before the start, take it out again.
	started := false
	defer func() {
		if !started {
			_ = deleteJob(ctx, c, jobName, run.JobCount)
		}
	}()

	step := abapStep(jobName, run.JobCount, report, r.Variant, params)
	added, err := c.Call(ctx, "BAPI_XBP_JOB_ADD_ABAP_STEP", step)
	if err != nil {
		return run, fmt.Errorf("BAPI_XBP_JOB_ADD_ABAP_STEP: %w", err)
	}
	if err := bapiError("BAPI_XBP_JOB_ADD_ABAP_STEP", added.Get("RETURN")); err != nil {
		return run, err
	}

	server, err := applicationServer(ctx, c)
	if err != nil {
		return run, err
	}
	start, err := c.Call(ctx, "BAPI_XBP_JOB_START_ASAP", rfc.Params{
		"JOBNAME": jobName, "JOBCOUNT": run.JobCount,
		"EXTERNAL_USER_NAME": xbpUser, "TARGET_SERVER": server,
	})
	if err != nil {
		return run, fmt.Errorf("BAPI_XBP_JOB_START_ASAP: %w", err)
	}
	if err := bapiError("BAPI_XBP_JOB_START_ASAP", start.Get("RETURN")); err != nil {
		return run, err
	}
	started = true

	if wait <= 0 {
		return run, nil
	}
	deadline := time.Now().Add(wait)
	for {
		status, err := jobStatus(ctx, c, run.JobName, run.JobCount)
		if err != nil {
			return run, fmt.Errorf("job %s / %s started, but %w", run.JobName, run.JobCount, err)
		}
		run.Status, run.StatusFor = status, jobStatusText(status)
		// P scheduled, S released, R running, Y ready — anything else is terminal.
		if status != "" && !strings.Contains("PSRY", strings.ToUpper(status)) {
			return run, nil
		}
		if time.Now().After(deadline) {
			return run, nil // still running; the caller has the job id to follow up
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// abapStep is the BAPI_XBP_JOB_ADD_ABAP_STEP input for one report step.
func abapStep(jobName, jobCount, report, variant string, params []ReportParam) rfc.Params {
	step := rfc.Params{
		"JOBNAME": jobName, "JOBCOUNT": jobCount,
		"EXTERNAL_USER_NAME": xbpUser, "ABAP_PROGRAM_NAME": report,
		// Without print parameters a background step writes its list nowhere and
		// TBTCP-LISTIDENT stays 0, so there is nothing to read afterwards.
		// ALLPRIPAR is the classic PRI_PARAMS set: hold the request on the default
		// device rather than printing it.
		"ALLPRIPAR": map[string]any{
			"PDEST": printDestination, // spool device
			"PRIMM": " ",              // do not print immediately
			"PRREL": " ",              // keep the request after printing
			"PEXPI": "8",              // days before it expires
			"LINSZ": 255,
			"LINCT": 65,
		},
	}
	if variant = strings.ToUpper(strings.TrimSpace(variant)); variant != "" {
		step["ABAP_VARIANT_NAME"] = variant
	}
	if rows := selectionRows(params); len(rows) > 0 {
		step["SELINFO"] = rows
	}
	return step
}

// deleteJob removes a job through XBP. It runs inside the caller's XMI session.
func deleteJob(ctx context.Context, c *rfc.Client, jobName, jobCount string) error {
	res, err := c.Call(ctx, "BAPI_XBP_JOB_DELETE", rfc.Params{
		"JOBNAME": jobName, "JOBCOUNT": jobCount, "EXTERNAL_USER_NAME": xbpUser,
	})
	if err != nil {
		return fmt.Errorf("BAPI_XBP_JOB_DELETE: %w", err)
	}
	return bapiError("BAPI_XBP_JOB_DELETE", res.Get("RETURN"))
}

// DeleteJob removes a job that has not started, or has ended.
func DeleteJob(ctx context.Context, c *rfc.Client, jobName, jobCount string) error {
	if err := xmiLogon(ctx, c); err != nil {
		return err
	}
	defer func() { _, _ = c.Call(ctx, "BAPI_XMI_LOGOFF", rfc.Params{"INTERFACE": "XBP"}) }()
	return deleteJob(ctx, c, jobName, jobCount)
}

// xbpUser is the external scheduler identity XBP records against the job.
const xbpUser = "vsp"

// xmiLogon opens the XMI session the XBP BAPIs require.
func xmiLogon(ctx context.Context, c *rfc.Client) error {
	res, err := c.Call(ctx, "BAPI_XMI_LOGON", rfc.Params{
		"EXTCOMPANY": "vsp", "EXTPRODUCT": "vibing-steampunk", "INTERFACE": "XBP", "VERSION": "3.0",
	})
	if err != nil {
		return fmt.Errorf("BAPI_XMI_LOGON: %w", err)
	}
	return bapiError("BAPI_XMI_LOGON", res.Get("RETURN"))
}

// applicationServer returns this instance's name, which XBP wants as the job's
// target server. RFC_SYSTEM_INFO reports it as RFCDEST (host_SID_nn).
func applicationServer(ctx context.Context, c *rfc.Client) (string, error) {
	info, err := c.Call(ctx, "RFC_SYSTEM_INFO", nil)
	if err != nil {
		return "", fmt.Errorf("resolving the target server: %w", err)
	}
	m, _ := info.Get("RFCSI_EXPORT").(map[string]any)
	server := strings.TrimSpace(fmt.Sprint(m["RFCDEST"]))
	if server == "" {
		return "", fmt.Errorf("could not determine the application server name")
	}
	return server, nil
}

// selectionRows turns parameters into XBP's SELINFO rows.
func selectionRows(params []ReportParam) []map[string]any {
	rows := make([]map[string]any, 0, len(params))
	for _, p := range params {
		rows = append(rows, map[string]any{
			"SELNAME": strings.ToUpper(p.Name),
			"KIND":    firstNonEmpty(strings.ToUpper(p.Kind), "P"),
			"SIGN":    firstNonEmpty(strings.ToUpper(p.Sign), "I"),
			"OPTION":  firstNonEmpty(strings.ToUpper(p.Option), "EQ"),
			"LOW":     p.Low,
			"HIGH":    p.High,
		})
	}
	return rows
}

// bapiError turns a BAPIRET2 of type E or A into a Go error.
func bapiError(call string, ret any) error {
	m, ok := ret.(map[string]any)
	if !ok {
		return nil
	}
	t := strings.ToUpper(strings.TrimSpace(fmt.Sprint(m["TYPE"])))
	if t != "E" && t != "A" {
		return nil
	}
	msg := strings.TrimSpace(fmt.Sprint(m["MESSAGE"]))
	if msg == "" {
		msg = fmt.Sprintf("%v%v", m["ID"], m["NUMBER"])
	}
	return fmt.Errorf("%s: %s", call, msg)
}

// JobStatus reads one job's TBTCO status letter and its reading in words.
func JobStatus(ctx context.Context, c *rfc.Client, jobName, jobCount string) (string, string, error) {
	status, err := jobStatus(ctx, c, jobName, jobCount)
	return status, jobStatusText(status), err
}

// jobStatus reads one job's TBTCO status.
func jobStatus(ctx context.Context, c *rfc.Client, jobName, jobCount string) (string, error) {
	where := fmt.Sprintf("JOBNAME = '%s' AND JOBCOUNT = '%s'", jobName, jobCount)
	rows, err := ReadTable(ctx, c, "TBTCO", where, []string{"STATUS"}, 1)
	if err != nil {
		return "", fmt.Errorf("reading job status: %w", err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	return strings.TrimSpace(rows[0]["STATUS"]), nil
}

// printDestination is the spool device a step prints to. LP01 exists on every
// system; the request is held rather than printed.
const printDestination = "LP01"

// ReadSpool returns a finished job's spool list through the XBP BAPIs, which need
// their own XMI session — hence the logon and logoff around the read. A job that
// produced no spool (TBTCP-LISTIDENT is zero) yields an empty string, not an error.
func ReadSpool(ctx context.Context, c *rfc.Client, jobName, jobCount string) (string, error) {
	return ReadSpoolStep(ctx, c, jobName, jobCount, 1)
}

// ReadSpoolStep reads the spool list of one step of a job.
func ReadSpoolStep(ctx context.Context, c *rfc.Client, jobName, jobCount string, step int) (string, error) {
	if err := xmiLogon(ctx, c); err != nil {
		return "", err
	}
	defer func() { _, _ = c.Call(ctx, "BAPI_XMI_LOGOFF", rfc.Params{"INTERFACE": "XBP"}) }()

	res, err := c.Call(ctx, "BAPI_XBP_JOB_SPOOLLIST_READ", rfc.Params{
		"JOBNAME": jobName, "JOBCOUNT": jobCount, "EXTERNAL_USER_NAME": xbpUser,
		"STEP_NUMBER": step, // XBP requires it
	})
	if err != nil {
		return "", fmt.Errorf("BAPI_XBP_JOB_SPOOLLIST_READ: %w", err)
	}
	var b strings.Builder
	for _, row := range res.Table("SPOOL_LIST") {
		for _, key := range []string{"LINE", "SPOOLLIST"} {
			if v, ok := row[key]; ok {
				fmt.Fprintln(&b, strings.TrimRight(fmt.Sprint(v), " "))
				break
			}
		}
	}
	return b.String(), nil
}

func asInt32(v any) int32 {
	switch n := v.(type) {
	case int32:
		return n
	case int64:
		return int32(n)
	case int:
		return int32(n)
	case float64:
		return int32(n)
	}
	return 0
}
