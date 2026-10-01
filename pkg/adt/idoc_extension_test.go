package adt

import (
	"context"
	"strings"
	"testing"
)

func releasedExtension(release string) *tableRunner {
	return &tableRunner{rows: map[string][]map[string]string{
		"EDCIM":  {{"IDOCTYP": "DELVRY07", "CLOSED": "X", "RELEASED": "758"}},
		"EDCIMT": {{"LANGUA": "E", "DESCRP": "Demo"}},
		"CIMSYN": {
			{"NR": "0002", "SEGTYP": "E1EDL20", "CIMSTP": "Z1SUB", "PARSEG": "Z1DEMO", "MUSTFL": "", "OCCMIN": "0000000001", "OCCMAX": "0000000099"},
			{"NR": "0001", "SEGTYP": "E1EDL20", "CIMSTP": "Z1DEMO", "PARSEG": "", "MUSTFL": "X", "OCCMIN": "0000000001", "OCCMAX": "0000000001"},
		},
		"TADIR": {{"DEVCLASS": "ZPKG"}},
		"CVERS": {{"RELEASE": release}},
	}}
}

func TestReadIDocExtension(t *testing.T) {
	c := &Client{config: &Config{}}
	ext, err := c.ReadIDocExtension(context.Background(), "zdelvry07", releasedExtension("758"))
	if err != nil {
		t.Fatal(err)
	}
	if ext.BasicType != "DELVRY07" || !ext.Closed || ext.Description != "Demo" || ext.Package != "ZPKG" {
		t.Errorf("header = %+v", ext)
	}
	want := []ExtensionSegment{
		{Segment: "Z1DEMO", Parent: "E1EDL20", Min: 1, Max: 1, Mandatory: true},
		{Segment: "Z1SUB", Parent: "Z1DEMO", Min: 1, Max: 99},
	}
	if !sameExtensionSegments(ext.Segments, want) {
		t.Errorf("segments = %+v", ext.Segments)
	}
}

// A segment below a basic-type segment refers to it at level 1; one below
// an added segment names it, keeps its reference and goes one level deeper.
func TestExtensionSyntax(t *testing.T) {
	rows, err := extensionSyntax([]ExtensionSegment{
		{Segment: "Z1DEMO", Parent: "E1EDL20", Min: 1, Max: 1},
		{Segment: "Z1SUB", Parent: "Z1DEMO", Min: 1, Max: 9},
		{Segment: "Z1OTHER", Parent: "E1EDL24", Min: 1, Max: 5},
		{Segment: "Z1DEEP", Parent: "Z1OTHER", Min: 1, Max: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := rows[0]; r.Ref != "E1EDL20" || r.Parent != "" || r.Level != 1 || !r.HasChildren {
		t.Errorf("row 1 = %+v", r)
	}
	if r := rows[1]; r.Ref != "E1EDL20" || r.Parent != "Z1DEMO" || r.ParentNr != 1 || r.Level != 2 || r.HasChildren {
		t.Errorf("row 2 = %+v", r)
	}
	if r := rows[2]; r.Ref != "E1EDL24" || r.Nr != 3 || r.Level != 2 {
		t.Errorf("row 3 = %+v, want level 2 for a repeating top segment", r)
	}
	if r := rows[3]; r.Ref != "E1EDL24" || r.Level != 3 || r.ParentNr != 3 {
		t.Errorf("row 4 = %+v", r)
	}
	if _, err := extensionSyntax([]ExtensionSegment{
		{Segment: "Z1SUB", Parent: "Z1DEMO", Min: 1, Max: 1},
		{Segment: "Z1DEMO", Parent: "E1EDL20", Min: 1, Max: 1},
	}); err == nil || !strings.Contains(err.Error(), "before its parent") {
		t.Errorf("child before parent: %v", err)
	}
}

// A released extension keeps what it has; changes to it are refused before
// anything runs, and so is one released in an earlier SAP release.
func TestChangeIDocExtension_Refusals(t *testing.T) {
	c := &Client{config: &Config{}}
	base := []ExtensionSegment{
		{Segment: "Z1DEMO", Parent: "E1EDL20", Min: 1, Max: 1, Mandatory: true},
		{Segment: "Z1SUB", Parent: "Z1DEMO", Min: 1, Max: 99},
	}
	for name, segs := range map[string][]ExtensionSegment{
		"dropped": base[:1],
		"changed": {base[0], {Segment: "Z1SUB", Parent: "Z1DEMO", Min: 1, Max: 5}},
	} {
		run := releasedExtension("758")
		_, err := c.ChangeIDocExtension(context.Background(), ExtensionChange{Name: "ZDELVRY07", Transport: "TR-EXAMPLE", Segments: segs}, run)
		if err == nil || !strings.Contains(err.Error(), "keeps its segments") {
			t.Errorf("%s: %v", name, err)
		}
		if run.ran {
			t.Errorf("%s: a report ran", name)
		}
	}
	added := append(append([]ExtensionSegment{}, base...), ExtensionSegment{Segment: "Z1NEW", Parent: "E1EDL24"})
	run := releasedExtension("757")
	_, err := c.ChangeIDocExtension(context.Background(), ExtensionChange{Name: "ZDELVRY07", Transport: "TR-EXAMPLE", Segments: added}, run)
	if err == nil || !strings.Contains(err.Error(), "note 844899") {
		t.Errorf("earlier release: %v", err)
	}
	open := true
	_, err = c.ChangeIDocExtension(context.Background(), ExtensionChange{Name: "ZDELVRY07", Release: &open}, releasedExtension("758"))
	if err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Errorf("nothing to change: %v", err)
	}
}

func TestExtensionReportSource(t *testing.T) {
	rows, _ := extensionSyntax([]ExtensionSegment{{Segment: "/NS/E1DEMO", Parent: "E1EDL20", Min: 1, Max: 9999999999, Mandatory: true}})
	src := extensionReportSource("ZTEMP_IEXT_1", extensionReport{
		Op: "CREATE", Name: "/NS/DELVRY07", BasicType: "DELVRY07", Description: "It's a demo",
		Package: "/NS/PKG", Transport: "TR-EXAMPLE", Syntax: rows, SetSyntax: true, Release: "X",
	})
	for _, want := range []string{
		"REPORT ztemp_iext_1.",
		"gc_cim TYPE edi_iapi00-cimtyp VALUE '/NS/DELVRY07'",
		"gs_in-descrp = 'It''s a demo'.",
		"gs_syn-refsegtyp = 'E1EDL20'.",
		"gs_syn-occmax = 9999999999.",
		"gs_syn-mustfl = 'X'.",
		"CALL FUNCTION 'EXTTYPE_CREATE'",
		"CALL FUNCTION 'EXTTYPE_UPDATE'",
		"CALL FUNCTION 'EXTTYPE_TRANSPORT'",
		"pi_activity = '43'",
		"IF gs_attr-released <> sy-saprl.",
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

func TestApplyExtensionLog(t *testing.T) {
	res := &ExtensionResult{Program: "ZTEMP_IEXT_1"}
	if err := applyExtensionLog(res, []string{"VSP-IEXT:STEP=REOPEN", "VSP-IEXT:STEP=UPDATE", "VSP-IEXT:CLOSED=X", "VSP-IEXT:RELEASED=758", "VSP-IEXT:OK=X"}); err != nil {
		t.Fatal(err)
	}
	if len(res.Steps) != 2 || !res.Closed || res.Released != "758" {
		t.Errorf("result = %+v", res)
	}
	if err := applyExtensionLog(&ExtensionResult{}, []string{"VSP-IEXT:ERR=EXTTYPE_CLOSE: refused (EA 1)"}); err == nil {
		t.Error("an ERR passed")
	}
}
