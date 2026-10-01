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
	// importErr, when set, ends the import call after TMS took the import:
	// TPALOG gains its rows, the caller gets the error.
	importErr error
}

func (s *fakeSystem) call(ctx context.Context, fm string, in rfc.Params) (exports, error) {
	s.calls = append(s.calls, fm)
	// As a real connection does, a call under a dead context gets nowhere.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		if s.importErr != nil {
			return nil, s.importErr
		}
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

	res, err := importRequests(context.Background(), sys.call, []string{"tr-example"}, "100", nil)
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

	res, err := importRequests(context.Background(), sys.call, []string{"TR-EXAMPLE"}, "100", nil)
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
			if _, err := importRequests(context.Background(), sys.call, tc.requests, tc.client, nil); err == nil {
				t.Fatal("accepted")
			}
			if len(sys.calls) != 0 {
				t.Errorf("called %v before refusing", sys.calls)
			}
		})
	}
}

// An import followed after the fact: TPALOG's steps for each request, only
// those of this run when since is given, with the worst return code.
func TestImportLogs_StepsSinceAGivenTime(t *testing.T) {
	sys := &fakeSystem{sid: "PRD", logBefore: []string{
		"TR-EXAMPLE|100|I|0000|20260101100000",
		"TR-EXAMPLE|ALL|L|0000|20260102100000",
		"TR-EXAMPLE|100|I|0008|20260102100005",
	}}
	logs, err := importLogs(context.Background(), sys.call, []string{"tr-example", "TR-OTHER"}, "20260102000000")
	if err != nil {
		t.Fatalf("importLogs: %v", err)
	}
	if len(logs) != 2 || logs[0].Request != "TR-EXAMPLE" || logs[1].Request != "TR-OTHER" {
		t.Fatalf("logs = %+v", logs)
	}
	if len(logs[0].Steps) != 2 || logs[0].MaxRC != "0008" {
		t.Errorf("TR-EXAMPLE = %+v, want the two steps since the 2nd and rc 0008", logs[0])
	}
	if len(logs[1].Steps) != 0 || logs[1].MaxRC != "" {
		t.Errorf("TR-OTHER = %+v, want no steps", logs[1])
	}
}

func TestImportLogs_RefusesBeforeReading(t *testing.T) {
	for name, tc := range map[string]struct {
		reqs  []string
		since string
	}{
		"no request":  {nil, ""},
		"bad request": {[]string{"X' OR '1'='1"}, ""},
		"bad since":   {[]string{"TR-EXAMPLE"}, "yesterday"},
		"9 digits":    {[]string{"TR-EXAMPLE"}, "202601021"},
		"13 digits":   {[]string{"TR-EXAMPLE"}, "2026010210000"},
	} {
		sys := &fakeSystem{sid: "PRD"}
		if _, err := importLogs(context.Background(), sys.call, tc.reqs, tc.since); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(sys.calls) != 0 {
			t.Errorf("%s: read before refusing", name)
		}
	}
}

// The request list is split over RFC_READ_TABLE's 72-character OPTIONS
// lines, which break only between tokens: eight requests must still fit.
func TestImportLogs_ManyRequestsFitTheOptionsLines(t *testing.T) {
	var reqs []string
	for i := 0; i < 8; i++ {
		reqs = append(reqs, "TR-EXAMPL"+string(rune('0'+i)))
	}
	var options []map[string]any
	call := func(_ context.Context, fm string, in rfc.Params) (exports, error) {
		if fm == "RFC_READ_TABLE" {
			options, _ = in["OPTIONS"].([]map[string]any)
		}
		return fakeExports{tables: map[string][]map[string]any{"FIELDS": {}}}, nil
	}
	if _, err := importLogs(context.Background(), call, reqs, ""); err != nil {
		t.Fatalf("importLogs with %d requests: %v", len(reqs), err)
	}
	if len(options) < 2 {
		t.Errorf("OPTIONS = %v, want the list split over several lines", options)
	}
}

// reopenOn gives a fresh connection to sys, counting how often it was asked.
func reopenOn(sys *fakeSystem, opened *int) reopenFn {
	return func(context.Context) (callFn, func(), error) {
		*opened++
		return sys.call, func() {}, nil
	}
}

// checkOutcomeUnknown asserts the shape every lost import shares: an
// *ImportOutcomeUnknownError that keeps its cause, a result marked unknown
// that names the requests and says to check TPALOG/STMS, and no word of a
// failure to retry.
func checkOutcomeUnknown(t *testing.T, res *ImportResult, err error, cause error, reqs ...string) *ImportOutcomeUnknownError {
	t.Helper()
	var unknown *ImportOutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v (%T), want an *ImportOutcomeUnknownError", err, err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err %v does not wrap the cause %v", err, cause)
	}
	if res == nil {
		t.Fatal("no result: the caller cannot tell the import may be running")
	}
	if res.Outcome != OutcomeUnknown || res.Imported {
		t.Errorf("outcome = %q imported = %v, want unknown and not imported", res.Outcome, res.Imported)
	}
	if res.System != "PRD" || res.Client != "100" || res.Submitted == "" {
		t.Errorf("result = %+v, want the system, client and submission time", res)
	}
	if len(res.Requests) != len(reqs) {
		t.Fatalf("requests = %+v, want %v", res.Requests, reqs)
	}
	for i, r := range reqs {
		if res.Requests[i].Request != r {
			t.Errorf("request %d = %q, want %q", i, res.Requests[i].Request, r)
		}
		if !strings.Contains(err.Error(), r) || !strings.Contains(res.Advice, r) {
			t.Errorf("%s missing from the error or the advice: %v / %s", r, err, res.Advice)
		}
	}
	for _, text := range []string{err.Error(), res.Advice} {
		if !strings.Contains(text, "TPALOG") || !strings.Contains(text, "STMS") {
			t.Errorf("does not point at TPALOG and STMS: %s", text)
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "retry") || strings.Contains(lower, "failed") {
			t.Errorf("reads as a failure to retry: %s", text)
		}
	}
	return unknown
}

// The import call times out after TMS took the import: the outcome is
// unknown, not a failure, and the result says what was submitted.
func TestImportRequests_ACallLostAfterSubmissionIsAnUnknownOutcome(t *testing.T) {
	for name, cause := range map[string]error{
		"timeout":          context.DeadlineExceeded,
		"connection drop":  errors.New("read tcp 10.0.0.1:3300: connection reset by peer"),
		"context canceled": context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			sys := &fakeSystem{sid: "PRD", importErr: cause}
			res, err := importRequests(context.Background(), sys.call, []string{"TR-A", "tr-b"}, "100", nil)
			u := checkOutcomeUnknown(t, res, err, cause, "TR-A", "TR-B")
			if res.Started != nil || u.Started != nil {
				t.Errorf("started = %v, want unknown without a recheck", res.Started)
			}
		})
	}
}

// With a way to log on again, TPALOG is read on a fresh connection -- even
// when the call's own context is the one that ran out -- and shows the steps
// this import added.
func TestImportRequests_ALostCallRechecksTPALOGOnAFreshConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sys := &fakeSystem{
		sid:       "PRD",
		logBefore: []string{"TR-A|100|I|0000|20260101100000"},
		logAfter: []string{
			"TR-A|100|I|0000|20260101100000",
			"TR-A|ALL|L|0000|20260102100000",
			"TR-A|100|I|0004|20260102100005",
		},
	}
	// The caller gives up while tp runs: the call's context is cancelled.
	sys.importErr = context.Canceled
	call := func(c context.Context, fm string, in rfc.Params) (exports, error) {
		out, err := sys.call(c, fm, in)
		if fm == "CTS_API_IMPORT_CHANGE_REQUEST" {
			cancel()
		}
		return out, err
	}
	opened := 0
	res, err := importRequests(ctx, call, []string{"TR-A"}, "100", reopenOn(sys, &opened))
	u := checkOutcomeUnknown(t, res, err, context.Canceled, "TR-A")
	if opened != 1 {
		t.Errorf("reopened %d times, want 1", opened)
	}
	if res.Started == nil || !*res.Started || u.Started == nil || !*u.Started {
		t.Fatalf("started = %v, want true: TPALOG holds new steps", res.Started)
	}
	if got := res.Requests[0]; len(got.Steps) != 2 || got.MaxRC != "0004" {
		t.Errorf("TR-A = %+v, want this import's two steps and rc 0004", got)
	}
	if !strings.Contains(err.Error(), "has started") {
		t.Errorf("error does not say the import started: %v", err)
	}
}

// A recheck that finds nothing new still does not call it a failure: TMS
// may not have handed the request to tp yet.
func TestImportRequests_ARecheckWithoutNewStepsStaysUnknown(t *testing.T) {
	sys := &fakeSystem{sid: "PRD", importErr: context.DeadlineExceeded,
		logBefore: []string{"TR-A|100|I|0000|20260101100000"},
		logAfter:  []string{"TR-A|100|I|0000|20260101100000"},
	}
	opened := 0
	res, err := importRequests(context.Background(), sys.call, []string{"TR-A"}, "100", reopenOn(sys, &opened))
	checkOutcomeUnknown(t, res, err, context.DeadlineExceeded, "TR-A")
	if res.Started == nil || *res.Started {
		t.Errorf("started = %v, want false", res.Started)
	}
	if len(res.Requests[0].Steps) != 0 {
		t.Errorf("steps = %+v, want none", res.Requests[0].Steps)
	}
}

// A recheck that cannot log on leaves the outcome unknown and says so.
func TestImportRequests_ARecheckThatCannotLogOnStaysUnknown(t *testing.T) {
	sys := &fakeSystem{sid: "PRD", importErr: context.DeadlineExceeded}
	reopen := func(context.Context) (callFn, func(), error) { return nil, nil, errors.New("gateway unreachable") }
	res, err := importRequests(context.Background(), sys.call, []string{"TR-A"}, "100", reopen)
	checkOutcomeUnknown(t, res, err, context.DeadlineExceeded, "TR-A")
	if res.Started != nil {
		t.Errorf("started = %v, want nil: TPALOG was not read", *res.Started)
	}
	if !strings.Contains(err.Error(), "could not be read again") {
		t.Errorf("error does not say TPALOG was not read: %v", err)
	}
}

// An error before the import call is not an unknown outcome: nothing was
// submitted.
func TestImportRequests_AnErrorBeforeSubmissionIsNotUnknown(t *testing.T) {
	call := func(context.Context, string, rfc.Params) (exports, error) { return nil, context.DeadlineExceeded }
	_, err := importRequests(context.Background(), call, []string{"TR-A"}, "100", nil)
	var unknown *ImportOutcomeUnknownError
	if err == nil || errors.As(err, &unknown) {
		t.Errorf("err = %v, want a plain error: the import was never submitted", err)
	}
}
