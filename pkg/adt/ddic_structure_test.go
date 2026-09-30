package adt

import "testing"

func TestStructureSourceName(t *testing.T) {
	for src, want := range map[string][2]string{
		"@EndUserText.label : 'x'\ndefine structure zdemo {\n  a : abap.char(1);\n}":                             {"ZDEMO", ""},
		"define   structure /par/s_demo { a : abap.char(1); }":                                                   {"/PAR/S_DEMO", ""},
		"@AbapCatalog.enhancement.category : #NOT_EXTENSIBLE\nextend type shp_vl10_item with /par/append_x {\n}": {"/PAR/APPEND_X", "SHP_VL10_ITEM"},
	} {
		name, extends, err := structureSourceName(src)
		if err != nil || name != want[0] || extends != want[1] {
			t.Errorf("%q: %s %s %v, want %v", src, name, extends, err, want)
		}
	}
	if _, _, err := structureSourceName("define table zdemo { }"); err == nil {
		t.Error("a table definition accepted as a structure")
	}
	if got := StructureURL("/PAR/S_DEMO"); got != "/sap/bc/adt/ddic/structures/%2Fpar%2Fs_demo" {
		t.Errorf("namespace URL %s", got)
	}
}
