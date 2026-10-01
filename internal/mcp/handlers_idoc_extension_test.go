package mcp

import "testing"

func TestExtensionSegments_ObjectsAndStrings(t *testing.T) {
	segs, given, err := extensionSegments(map[string]any{"segments": []any{
		map[string]any{"segment": "Z1DEMO", "parent": "E1EDL20", "max": float64(99), "mandatory": true},
		"Z1SUB Z1DEMO",
	}})
	if err != nil || !given || len(segs) != 2 {
		t.Fatalf("%+v %v %v", segs, given, err)
	}
	if segs[0].Max != 99 || !segs[0].Mandatory || segs[1].Parent != "Z1DEMO" {
		t.Errorf("%+v", segs)
	}
	if _, _, err := extensionSegments(map[string]any{"segments": []any{"Z1ONLY"}}); err == nil {
		t.Error("a segment without a parent was accepted")
	}
}
