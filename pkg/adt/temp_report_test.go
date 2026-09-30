package adt

import (
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
		{"OBJ_NAME": "ZTEMP_SXCI_12345678"},      // the older name without a full time stamp
		{"OBJ_NAME": "ZTEMP_OTHER_1700000000000"}, // somebody else's prefix
		{"OBJ_NAME": "ZTEMP_SXCI_17000000000AB"},
	}
	got := staleTempReports(rows, current, now)
	if len(got) != 1 || got[0] != old {
		t.Errorf("stale = %v, want [%s]", got, old)
	}
}
