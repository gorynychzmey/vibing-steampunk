package saprfc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oisee/open-rfc-go/rfc"
)

// fakeExports stands in for rfc.Result.
type fakeExports struct {
	scalars map[string]any
	tables  map[string][]map[string]any
}

func (f fakeExports) Get(name string) any                { return f.scalars[name] }
func (f fakeExports) Table(name string) []map[string]any { return f.tables[name] }

// fakeSystem answers the three function modules an import uses, with a TPALOG
// that gains rows when the import runs.
type fakeSystem struct {
	sid        string
	logBefore  []string // TPALOG rows as "TRKORR|TRCLI|TRSTEP|RETCODE|TRTIME"
	logAfter   []string
	importRC   string
	importMsg  string
	perRequest string
	calls      []string
	imported   bool
	importArgs rfc.Params
}

func (s *fakeSystem) call(_ context.Context, fm string, in rfc.Params) (exports, error) {
	s.calls = append(s.calls, fm)
	switch fm {
	case "RFC_SYSTEM_INFO":
		return fakeExports{scalars: map[string]any{"RFCSI_EXPORT": map[string]any{"RFCSYSID": s.sid}}}, nil
	case "RFC_READ_TABLE":
		rows := s.logBefore
		if s.imported {
			rows = s.logAfter
		}
		var data []map[string]any
		for _, r := range rows {
			data = append(data, map[string]any{"WA": r})
		}
		var fields []map[string]any
		for _, f := range in["FIELDS"].([]map[string]any) {
			fields = append(fields, map[string]any{"FIELDNAME": f["FIELDNAME"]})
		}
		return fakeExports{tables: map[string][]map[string]any{"DATA": data, "FIELDS": fields}}, nil
	case "CTS_API_IMPORT_CHANGE_REQUEST":
		s.imported = true
		s.importArgs = in
		var reqs []map[string]any
		for _, r := range in["REQUESTS"].([]map[string]any) {
			reqs = append(reqs, map[string]any{"REQUEST": r["REQUEST"], "RETCODE": s.perRequest})
		}
		return fakeExports{
			scalars: map[string]any{"RETCODE": s.importRC, "MESSAGE": s.importMsg},
			tables:  map[string][]map[string]any{"REQUESTS": reqs},
		}, nil
	}
	return nil, errors.New("unexpected " + fm)
}

func TestImportRequests_ImportsIntoTheConnectedSystemAndReportsTheNewSteps(t *testing.T) {
	sys := &fakeSystem{
		sid: "QAS",
		// An earlier run of the same request must not be reported as this one.
		logBefore: []string{"TR-EXAMPLE|100|I|0000|20260101100000"},
		logAfter: []string{
			"TR-EXAMPLE|100|I|0000|20260101100000",
			"TR-EXAMPLE|ALL|L|0000|20260102100000",
			"TR-EXAMPLE|100|I|0004|20260102100005",
			"TR-EXAMPLE|ALL|G|0000|20260102100009",
		},
		importRC: "000", importMsg: "Request TR-EXAMPLE imported into system QAS client 100", perRequest: "000",
	}

	res, err := importRequests(context.Background(), sys.call, []string{"tr-example"}, "100")
	if err != nil {
		t.Fatalf("importRequests: %v", err)
	}
	if got := sys.importArgs["SYSTEM"]; got != "QAS" {
		t.Errorf("SYSTEM = %v, want the connected system QAS", got)
	}
	if got := sys.importArgs["CLIENT"]; got != "100" {
		t.Errorf("CLIENT = %v, want 100", got)
	}
	if !res.Imported || res.System != "QAS" || len(res.Requests) != 1 {
		t.Fatalf("result = %+v", res)
	}
	r := res.Requests[0]
	if r.Request != "TR-EXAMPLE" || r.RetCode != "000" {
		t.Errorf("request = %+v", r)
	}
	if len(r.Steps) != 3 {
		t.Fatalf("steps = %+v, want the three of this run only", r.Steps)
	}
	if r.MaxRC != "0004" {
		t.Errorf("maxRC = %q, want 0004", r.MaxRC)
	}
}

func TestImportRequests_AFailedImportIsAnErrorWithTheSystemsMessage(t *testing.T) {
	sys := &fakeSystem{sid: "QAS", importRC: "012", importMsg: "Could not start import", perRequest: ""}

	res, err := importRequests(context.Background(), sys.call, []string{"TR-EXAMPLE"}, "100")
	if err == nil {
		t.Fatal("a failed import was reported as success")
	}
	if !strings.Contains(err.Error(), "Could not start import") || !strings.Contains(err.Error(), "012") {
		t.Errorf("error lacks the system's code and message: %v", err)
	}
	if res == nil || res.Imported {
		t.Errorf("result = %+v, want Imported=false", res)
	}
}

func TestImportRequests_RefusesBeforeCalling(t *testing.T) {
	for _, tc := range []struct {
		name     string
		requests []string
		client   string
	}{
		{"no request", nil, "100"},
		{"no client", []string{"TR-EXAMPLE"}, ""},
		{"not a request number", []string{"X' OR '1'='1"}, "100"},
		{"bad client", []string{"TR-EXAMPLE"}, "1O0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys := &fakeSystem{sid: "QAS", importRC: "000"}
			if _, err := importRequests(context.Background(), sys.call, tc.requests, tc.client); err == nil {
				t.Fatal("accepted")
			}
			if len(sys.calls) != 0 {
				t.Errorf("called %v before refusing", sys.calls)
			}
		})
	}
}
