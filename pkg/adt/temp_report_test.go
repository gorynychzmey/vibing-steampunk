package adt

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTempReportName_CarriesItsCreationTime(t *testing.T) {
	now := time.UnixMilli(1790000000123)
	name := tempReportName("ZTEMP_SEGM_", now)
	if name != "ZTEMP_SEGM_1790000000123" {
		t.Fatalf("name = %s", name)
	}
	if len(name) > 30 {
		t.Errorf("%s is longer than a program name may be", name)
	}
	got, ok := tempReportCreated(name)
	if !ok || !got.Equal(now) {
		t.Errorf("tempReportCreated(%s) = %v %v", name, got, ok)
	}
}

// Only vsp's own names, older than the limit, and never the report in use.
func TestStaleTempReports(t *testing.T) {
	now := time.UnixMilli(1790000000000)
	old := tempReportName("ZTEMP_SXCI_", now.Add(-2*time.Hour))
	fresh := tempReportName("ZTEMP_SXCD_", now.Add(-5*time.Minute))
	current := tempReportName("ZTEMP_SEGM_", now.Add(-3*time.Hour))
	rows := []map[string]string{
		{"OBJ_NAME": old + "  "},
		{"OBJ_NAME": fresh},
		{"OBJ_NAME": current},
		{"OBJ_NAME": "ZTEMP_SXCI_12345678"},       // the older name without a full time stamp
		{"OBJ_NAME": "ZTEMP_OTHER_1700000000000"}, // somebody else's prefix
		{"OBJ_NAME": "ZTEMP_SXCI_17000000000AB"},
	}
	got := staleTempReports(rows, current, now)
	if len(got) != 1 || got[0] != old {
		t.Errorf("stale = %v, want [%s]", got, old)
	}
}

// staleRunner answers the sweep's TADIR, TBTCP and TBTCO reads.
type staleRunner struct {
	steps map[string][]map[string]string // TBTCP rows by program
	jobs  map[string]string              // TBTCO status by job count
}

func (r *staleRunner) RunReport(context.Context, string) ([]string, error) { return nil, nil }

func (r *staleRunner) ReadTable(_ context.Context, table, where string, _ []string, _ int) ([]map[string]string, error) {
	switch table {
	case "TBTCP":
		for prog, rows := range r.steps {
			if strings.Contains(where, prog) {
				return rows, nil
			}
		}
	case "TBTCO":
		for count, status := range r.jobs {
			if strings.Contains(where, count) {
				return []map[string]string{{"STATUS": status}}, nil
			}
		}
	}
	return nil, nil
}

// A job that has not ended keeps its report, however old the report is.
func TestReportInUse(t *testing.T) {
	run := &staleRunner{
		steps: map[string][]map[string]string{
			"ZTEMP_SXCI_1": {{"JOBNAME": "VSP_ZTEMP_SXCI_1", "JOBCOUNT": "11111111"}},
			"ZTEMP_SXCI_2": {{"JOBNAME": "VSP_ZTEMP_SXCI_2", "JOBCOUNT": "22222222"}},
		},
		jobs: map[string]string{"11111111": "S", "22222222": "F"},
	}
	for prog, want := range map[string]bool{"ZTEMP_SXCI_1": true, "ZTEMP_SXCI_2": false, "ZTEMP_SXCI_3": false} {
		got, err := reportInUse(context.Background(), run, prog)
		if err != nil || got != want {
			t.Errorf("reportInUse(%s) = %v %v, want %v", prog, got, err, want)
		}
	}
}
