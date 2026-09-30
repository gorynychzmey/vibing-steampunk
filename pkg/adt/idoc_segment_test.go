package adt

import (
	"context"
	"strings"
	"testing"
)

// tableRunner answers ReadTable from canned rows per table; RunReport is not
// expected to be reached.
type tableRunner struct {
	rows map[string][]map[string]string
	ran  bool
}

func (r *tableRunner) RunReport(context.Context, string) ([]string, error) {
	r.ran = true
	return nil, nil
}

func (r *tableRunner) ReadTable(_ context.Context, table, _ string, _ []string, _ int) ([]map[string]string, error) {
	return r.rows[table], nil
}

func releasedSegment() *tableRunner {
	return &tableRunner{rows: map[string][]map[string]string{
		"EDISEGMENT": {{"QUALIFIER": ""}},
		"EDISEGT":    {{"LANGUA": "D", "DESCRP": "Demo"}, {"LANGUA": "E", "DESCRP": "Demo EN"}},
		"EDISDEF": {
			{"VERSION": "001", "SEGDEF": "Z1DEMO001", "RELEASED": "758", "CLOSED": "", "FIELDNUM": "0003"},
			{"VERSION": "000", "SEGDEF": "Z1DEMO000", "RELEASED": "750", "CLOSED": "X", "FIELDNUM": "0002"},
		},
		"EDSAPPL": {
			{"POS": "0002", "FIELDNAME": "FLAG", "ROLLNAME": "CHAR1", "EXPLENG": "000001"},
			{"POS": "0001", "FIELDNAME": "MATNR", "ROLLNAME": "MATNR", "EXPLENG": "000040"},
			{"POS": "0003", "FIELDNAME": "STAMP", "ROLLNAME": "TIMESTAMP", "EXPLENG": "000015"},
		},
		"TADIR": {{"DEVCLASS": "ZPKG"}},
	}}
}

func TestReadIDocSegment_OrdersFieldsAndVersions(t *testing.T) {
	c := &Client{config: &Config{}}
	seg, err := c.ReadIDocSegment(context.Background(), "z1demo", releasedSegment())
	if err != nil {
		t.Fatal(err)
	}
	if seg.Description != "Demo EN" || seg.Package != "ZPKG" {
		t.Errorf("header = %+v", seg)
	}
	if len(seg.Fields) != 3 || seg.Fields[0].Name != "MATNR" || seg.Fields[2].Length != 15 {
		t.Errorf("fields = %+v", seg.Fields)
	}
	if len(seg.Versions) != 2 || seg.Versions[0].Version != "000" || !seg.Versions[0].Closed {
		t.Errorf("versions = %+v", seg.Versions)
	}
	if n := seg.frozenFields(); n != 2 {
		t.Errorf("frozen = %d, want 2", n)
	}
}

// A released field may not be moved, renamed or retyped; the open version's
// own field may.
func TestChangeIDocSegment_KeepsReleasedFields(t *testing.T) {
	c := &Client{config: &Config{}}
	for name, fields := range map[string][]SegmentField{
		"dropped":  {{Name: "MATNR", DataElement: "MATNR"}},
		"swapped":  {{Name: "FLAG", DataElement: "CHAR1"}, {Name: "MATNR", DataElement: "MATNR"}},
		"retyped":  {{Name: "MATNR", DataElement: "MATNR"}, {Name: "FLAG", DataElement: "CHAR10"}},
		"renamed":  {{Name: "MATNR", DataElement: "MATNR"}, {Name: "FLAG2", DataElement: "CHAR1"}},
	} {
		run := releasedSegment()
		_, err := c.ChangeIDocSegment(context.Background(), SegmentChange{Name: "Z1DEMO", Transport: "A4HK900001", Fields: fields}, run)
		if err == nil || !strings.Contains(err.Error(), "only be added to") {
			t.Errorf("%s: err = %v", name, err)
		}
		if run.ran {
			t.Errorf("%s: a report was run", name)
		}
	}
}

func TestChangeIDocSegment_NothingToChange(t *testing.T) {
	c := &Client{config: &Config{}}
	same := []SegmentField{{Name: "MATNR", DataElement: "MATNR"}, {Name: "FLAG", DataElement: "CHAR1"}, {Name: "STAMP", DataElement: "TIMESTAMP"}}
	open := false
	_, err := c.ChangeIDocSegment(context.Background(), SegmentChange{Name: "Z1DEMO", Transport: "A4HK900001", Fields: same, Release: &open}, releasedSegment())
	if err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Errorf("err = %v", err)
	}
}

func TestChangeIDocSegment_NeedsATransportForATransportablePackage(t *testing.T) {
	c := &Client{config: &Config{}}
	rel := true
	_, err := c.ChangeIDocSegment(context.Background(), SegmentChange{Name: "Z1DEMO", Release: &rel}, releasedSegment())
	if err == nil || !strings.Contains(err.Error(), "transport request is required") {
		t.Errorf("err = %v", err)
	}
}

func TestSegmentReportSource(t *testing.T) {
	src := segmentReportSource("ZTEMP_SEGM_1", segmentReport{
		Op: "CREATE", Name: "/NS/E1DEMO", Description: "It's a demo", Package: "/NS/PKG", Transport: "A4HK900001",
		Fields:    []SegmentField{{Name: "MATNR", DataElement: "MATNR"}, {Name: "WAERS", DataElement: "WAERS", ISOCode: true}},
		SetFields: true, Release: "X",
	})
	for _, want := range []string{
		"REPORT ztemp_segm_1.",
		"gc_seg TYPE edisegmhd-segtyp VALUE '/NS/E1DEMO'",
		"gc_release TYPE c VALUE 'X'",
		"gs_hd-descrp = 'It''s a demo'.",
		"gs_stru-pos = 2.",
		"gs_stru-isocode = 'X'.",
		"gv_order = 'A4HK900001'.",
		"CALL FUNCTION 'SEGMENT_CREATE'",
		"CALL FUNCTION 'SEGMENTDEFINITION_APPEND'",
		"IF gs_sdef-released = sy-saprl.",
		"CALL FUNCTION 'SEGMENT_DELETE'",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("source lacks %q", want)
		}
	}
	for i, line := range strings.Split(src, "\n") {
		if len(line) > 255 {
			t.Errorf("line %d is %d characters long", i+1, len(line))
		}
	}
}

func TestApplySegmentLog(t *testing.T) {
	res := &SegmentResult{Program: "ZTEMP_SEGM_1"}
	err := applySegmentLog(res, []string{
		"Job started",
		"VSP-SEGM:STEP=UNCLOSE Z1DEMO000",
		"VSP-SEGM:TASK=A4HK900002",
		"VSP-SEGM:STEP=MODIFY Z1DEMO000",
		"VSP-SEGM:STEP=CLOSE Z1DEMO000",
		"VSP-SEGM:DEF=Z1DEMO000",
		"VSP-SEGM:VERSION=000",
		"VSP-SEGM:CLOSED=X",
		"VSP-SEGM:RELEASED=758",
		"VSP-SEGM:OK=X",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Steps) != 3 || !res.Closed || res.Released != "758" || res.Task != "A4HK900002" || res.Definition != "Z1DEMO000" {
		t.Errorf("result = %+v", res)
	}
	res = &SegmentResult{Program: "ZTEMP_SEGM_1"}
	if err := applySegmentLog(res, []string{"VSP-SEGM:ERR=SEGMENTDEFINITION_CLOSE: already released (EA 123)"}); err == nil || !strings.Contains(err.Error(), "already released") {
		t.Errorf("err = %v", err)
	}
	if err := applySegmentLog(&SegmentResult{Program: "P"}, []string{"Job cancelled"}); err == nil {
		t.Error("a log without OK passed")
	}
}

func TestValidateSegmentCreate(t *testing.T) {
	ok := SegmentCreate{Name: "Z1DEMO", Description: "Demo", Package: "$TMP", Fields: []SegmentField{{Name: "A", DataElement: "CHAR1"}}}
	if err := validateSegmentCreate(ok); err != nil {
		t.Fatalf("valid input refused: %v", err)
	}
	for name, mutate := range map[string]func(*SegmentCreate){
		"no fields":            func(o *SegmentCreate) { o.Fields = nil },
		"quote in field":       func(o *SegmentCreate) { o.Fields = []SegmentField{{Name: "A'", DataElement: "CHAR1"}} },
		"field twice":          func(o *SegmentCreate) { o.Fields = append(o.Fields, o.Fields[0]) },
		"no description":       func(o *SegmentCreate) { o.Description = "" },
		"transportable, no TR": func(o *SegmentCreate) { o.Package = "ZPKG" },
		"name too long":        func(o *SegmentCreate) { o.Name = strings.Repeat("Z", 28) },
	} {
		o := ok
		mutate(&o)
		if err := validateSegmentCreate(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
