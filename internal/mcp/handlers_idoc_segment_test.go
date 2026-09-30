package mcp

import "testing"

func TestSegmentFields_TakesObjectsAndStrings(t *testing.T) {
	fields, given, err := segmentFields(map[string]any{"fields": []any{
		map[string]any{"name": "WAERS", "data_element": "WAERS", "iso_code": true},
		"FLAG CHAR1",
	}})
	if err != nil || !given || len(fields) != 2 {
		t.Fatalf("fields = %+v %v %v", fields, given, err)
	}
	if !fields[0].ISOCode || fields[1].Name != "FLAG" || fields[1].DataElement != "CHAR1" {
		t.Errorf("fields = %+v", fields)
	}
	if _, given, _ := segmentFields(map[string]any{}); given {
		t.Error("no fields reported as given")
	}
	if _, _, err := segmentFields(map[string]any{"fields": []any{"FLAG"}}); err == nil {
		t.Error("a field without a data element was accepted")
	}
	fields, _, err = segmentFields(map[string]any{"fields": `["A CHAR1"]`})
	if err != nil || len(fields) != 1 {
		t.Errorf("JSON string: %+v %v", fields, err)
	}
}
