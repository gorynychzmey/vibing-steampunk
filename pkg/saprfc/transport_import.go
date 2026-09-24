package saprfc

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/oisee/open-rfc-go/rfc"
)

// Importing a released request is what STMS_IMPORT does in the target system.
// CTS_API_IMPORT_CHANGE_REQUEST is SAP's remote-enabled CTS API for it: it
// hands the request to TMS, which runs tp. Called in the target system itself,
// TMS works locally under the caller's own logon. Called from the domain
// controller for another system, TMS needs that system's TMSSUP destination,
// which asks for a logon a remote call cannot give -- so ImportRequests always
// imports into the system it is connected to.

// exports is what a call returns; rfc.Result is one.
type exports interface {
	Get(name string) any
	Table(name string) []map[string]any
}

type callFn func(ctx context.Context, fm string, in rfc.Params) (exports, error)

func clientCall(c *rfc.Client) callFn {
	return func(ctx context.Context, fm string, in rfc.Params) (exports, error) {
		return c.Call(ctx, fm, in)
	}
}

// ImportStep is one tp step of an import, as TPALOG records it.
type ImportStep struct {
	Client  string `json:"client"`
	Step    string `json:"step"`
	RetCode string `json:"retcode"`
	Time    string `json:"time"`
}

// ImportedRequest is one request of an import.
type ImportedRequest struct {
	Request string `json:"request"`
	// RetCode is CTS_API_IMPORT_CHANGE_REQUEST's code for the request:
	// 000 tp rc 0-7, 008 rc 8, 012 anything else.
	RetCode string `json:"retcode"`
	// Steps are the TPALOG rows this import added, and MaxRC the worst of them.
	Steps []ImportStep `json:"steps,omitempty"`
	MaxRC string       `json:"maxRc,omitempty"`
}

// ImportResult is what an import did.
type ImportResult struct {
	System   string            `json:"system"`
	Client   string            `json:"client"`
	RetCode  string            `json:"retcode"`
	Message  string            `json:"message"`
	Imported bool              `json:"imported"`
	Requests []ImportedRequest `json:"requests"`
}

var (
	requestPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	clientPattern  = regexp.MustCompile(`^[0-9]{3}$`)
)

// ImportRequests imports released requests into the connected system, in
// client, through TMS -- as STMS_IMPORT does there -- and reports the tp steps
// the import added.
func ImportRequests(ctx context.Context, c *rfc.Client, requests []string, client string) (*ImportResult, error) {
	return importRequests(ctx, clientCall(c), requests, client)
}

func importRequests(ctx context.Context, call callFn, requests []string, client string) (*ImportResult, error) {
	var reqs []string
	for _, r := range requests {
		r = strings.ToUpper(strings.TrimSpace(r))
		if !requestPattern.MatchString(r) {
			return nil, fmt.Errorf("%q is not a request number", r)
		}
		reqs = append(reqs, r)
	}
	if len(reqs) == 0 {
		return nil, fmt.Errorf("at least one request to import is required")
	}
	if !clientPattern.MatchString(client) {
		return nil, fmt.Errorf("a three-digit target client is required, got %q", client)
	}

	info, err := call(ctx, "RFC_SYSTEM_INFO", rfc.Params{})
	if err != nil {
		return nil, fmt.Errorf("reading the system ID: %w", err)
	}
	sid := ""
	if m, ok := info.Get("RFCSI_EXPORT").(map[string]any); ok {
		sid = strings.TrimSpace(fmt.Sprint(m["RFCSYSID"]))
	}
	if sid == "" {
		return nil, fmt.Errorf("the connected system did not name itself (RFC_SYSTEM_INFO)")
	}

	before, err := readImportLog(ctx, call, reqs)
	if err != nil {
		return nil, fmt.Errorf("reading TPALOG before the import: %w", err)
	}

	in := rfc.Params{"SYSTEM": sid, "CLIENT": client}
	var table []map[string]any
	for _, r := range reqs {
		table = append(table, map[string]any{"REQUEST": r})
	}
	in["REQUESTS"] = table
	out, err := call(ctx, "CTS_API_IMPORT_CHANGE_REQUEST", in)
	if err != nil {
		return nil, fmt.Errorf("CTS_API_IMPORT_CHANGE_REQUEST: %w", err)
	}

	res := &ImportResult{
		System:  sid,
		Client:  client,
		RetCode: strings.TrimSpace(fmt.Sprint(out.Get("RETCODE"))),
		Message: strings.TrimSpace(fmt.Sprint(out.Get("MESSAGE"))),
	}
	perRequest := map[string]string{}
	for _, row := range out.Table("REQUESTS") {
		perRequest[strings.TrimSpace(fmt.Sprint(row["REQUEST"]))] = strings.TrimSpace(fmt.Sprint(row["RETCODE"]))
	}

	after, lerr := readImportLog(ctx, call, reqs)
	for _, r := range reqs {
		ir := ImportedRequest{Request: r, RetCode: perRequest[r]}
		if lerr == nil {
			for _, st := range after[r] {
				if !containsStep(before[r], st) {
					ir.Steps = append(ir.Steps, st)
					if st.RetCode > ir.MaxRC {
						ir.MaxRC = st.RetCode
					}
				}
			}
		}
		res.Requests = append(res.Requests, ir)
	}

	if res.RetCode != "000" {
		return res, fmt.Errorf("import into %s client %s failed: retcode %s (%s): %s",
			sid, client, res.RetCode, importRetCodeText(res.RetCode), res.Message)
	}
	res.Imported = true
	if lerr != nil {
		return res, fmt.Errorf("imported, but reading TPALOG afterwards failed: %w", lerr)
	}
	return res, nil
}

// readImportLog reads the TPALOG rows of the requests, by request, in order.
func readImportLog(ctx context.Context, call callFn, reqs []string) (map[string][]ImportStep, error) {
	quoted := make([]string, len(reqs))
	for i, r := range reqs {
		quoted[i] = "'" + r + "'"
	}
	rows, err := readTable(ctx, call, "TPALOG", "TRKORR IN ("+strings.Join(quoted, ",")+")",
		[]string{"TRKORR", "TRCLI", "TRSTEP", "RETCODE", "TRTIME"}, 0)
	if err != nil {
		return nil, err
	}
	out := map[string][]ImportStep{}
	for _, row := range rows {
		k := row["TRKORR"]
		out[k] = append(out[k], ImportStep{Client: row["TRCLI"], Step: row["TRSTEP"], RetCode: row["RETCODE"], Time: row["TRTIME"]})
	}
	for k := range out {
		sort.SliceStable(out[k], func(i, j int) bool { return out[k][i].Time < out[k][j].Time })
	}
	return out, nil
}

func containsStep(steps []ImportStep, s ImportStep) bool {
	for _, x := range steps {
		if x == s {
			return true
		}
	}
	return false
}

// importRetCodeText explains CTS_API_IMPORT_CHANGE_REQUEST's RETCODE.
func importRetCodeText(rc string) string {
	switch rc {
	case "000":
		return "imported"
	case "008":
		return "TMS could not start the import"
	case "009":
		return "tp finished with return code 8"
	case "010":
		return "tp finished with an error above 8"
	case "011":
		return "no authorization"
	case "012":
		return "TMS reported another error; see the TMS alert viewer"
	case "013":
		return "the import log could not be read"
	case "001", "002", "003", "004", "005", "006", "007":
		return "the target system or client was not accepted"
	}
	return "unknown code"
}
