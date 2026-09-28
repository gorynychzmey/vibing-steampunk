package mcp

import (
	"context"
	"testing"
)

func TestFunctionGroupOf(t *testing.T) {
	for pool, want := range map[string]string{
		"SAPLW61V":     "W61V",
		"saplzdemo ":   "ZDEMO",
		"/NS/SAPLDEMO": "/NS/DEMO",
	} {
		if got := functionGroupOf(pool); got != want {
			t.Errorf("%q: %q, want %q", pool, got, want)
		}
	}
}

func TestEnhancedObjectURL_ByName(t *testing.T) {
	s := &Server{}
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"url as given":   {map[string]any{"object_url": "/sap/bc/adt/functions/groups/zdemo"}, "/sap/bc/adt/functions/groups/zdemo"},
		"function group": {map[string]any{"function_group": "/NS/DEMO"}, "/sap/bc/adt/functions/groups/%2Fns%2Fdemo"},
		"program":        {map[string]any{"program": "ZREPORT"}, "/sap/bc/adt/programs/programs/zreport"},
		"class":          {map[string]any{"class": "ZCL_DEMO"}, "/sap/bc/adt/oo/classes/zcl_demo"},
	} {
		got, err := s.enhancedObjectURL(context.Background(), tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q %v, want %q", name, got, err, tc.want)
		}
	}
	if _, err := s.enhancedObjectURL(context.Background(), map[string]any{}); err == nil {
		t.Error("no object accepted")
	}
}
