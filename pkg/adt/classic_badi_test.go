package adt

import (
	"strings"
	"testing"
)

func TestClassicBadiCreateSource(t *testing.T) {
	src := classicBadiCreateSource("ZTEMP_SXCI_1", ClassicBadiImplementation{
		Name: "ZIMP", Badi: "BADI_X", Description: "It's a demo", Package: "ZPKG",
		Transport: "A4HK900001", Language: "E", Filters: []string{"F1", "F'2"}, Activate: true,
	})
	for _, want := range []string{
		"REPORT ztemp_sxci_1.",
		"gc_imp TYPE exit_imp VALUE 'ZIMP'",
		"gc_exit TYPE exit_def VALUE 'BADI_X'",
		"gv_iso = 'E'.",
		"gc_activate TYPE seex_boolean VALUE 'X'",
		"gv_text = 'It''s a demo'.",
		"gs_flt-flt_val = 'F''2'.",
		"gv_korr = 'A4HK900001'.",
		"CALL FUNCTION 'SXO_IMPL_ACTIVE'",
		"FORM fail_text",
		"PERFORM record_class USING gv_class gv_pkg gc_langu CHANGING gv_korr.",
		"wi_remove_genflag = 'X'",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("create source lacks %q", want)
		}
	}
	for i, line := range strings.Split(src, "\n") {
		if len(line) > 255 {
			t.Errorf("line %d is %d characters long", i+1, len(line))
		}
	}
}

func TestClassicBadiDeleteSource(t *testing.T) {
	src := classicBadiDeleteSource("ZTEMP_SXCD_1", "ZIMP", "$TMP", "", true)
	for _, want := range []string{
		"gc_keep_class TYPE seex_boolean VALUE 'X'",
		"gv_pkg = '$TMP'.",
		"wi_delete_tadir_entry = 'X'",
		"iv_delflag = 'X'",
		"gv_obj TYPE tadir-obj_name",
		"PERFORM record_class USING gv_class gv_pkg gv_mast CHANGING gv_korr.",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("delete source lacks %q", want)
		}
	}
}

func TestValidateClassicBadi(t *testing.T) {
	ok := ClassicBadiImplementation{Name: "ZIMP", Badi: "BADI_X", Package: "$TMP", Language: "D"}
	if err := validateClassicBadi(ok); err != nil {
		t.Fatalf("valid input refused: %v", err)
	}
	for name, mutate := range map[string]func(*ClassicBadiImplementation){
		"quote in name":             func(o *ClassicBadiImplementation) { o.Name = "Z'IMP" },
		"no badi":                   func(o *ClassicBadiImplementation) { o.Badi = "" },
		"transportable, no TR":      func(o *ClassicBadiImplementation) { o.Package = "ZPKG" },
		"long description":          func(o *ClassicBadiImplementation) { o.Description = strings.Repeat("x", 61) },
		"line break in filter":      func(o *ClassicBadiImplementation) { o.Filters = []string{"a\nb"} },
		"language is not a key":     func(o *ClassicBadiImplementation) { o.Language = "DEU" },
		"name longer than SXC_ATTR": func(o *ClassicBadiImplementation) { o.Name = strings.Repeat("Z", 21) },
	} {
		o := ok
		mutate(&o)
		if err := validateClassicBadi(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestApplyClassicBadiLog(t *testing.T) {
	res := &ClassicBadiResult{Program: "ZTEMP_SXCI_1"}
	err := applyClassicBadiLog(res, []string{
		"Job started",
		"VSP-SXCI:BADI=BADI_X",
		"VSP-SXCI:WARN=The BAdI has menu or screen enhancements",
		"VSP-SXCI:CLASS=ZCL_IM_IMP",
		"VSP-SXCI:KORR=A4HK900001",
		"VSP-SXCI:ACTIVE=X",
		"VSP-SXCI:OK=",
		"Job finished",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Class != "ZCL_IM_IMP" || res.Transport != "A4HK900001" || !res.Active || len(res.Warnings) != 1 {
		t.Errorf("parsed %+v", res)
	}

	err = applyClassicBadiLog(&ClassicBadiResult{}, []string{"VSP-SXCI:ERR=RS_CORR_INSERT: Request A4HK900001 is released"})
	if err == nil || !strings.Contains(err.Error(), "is released") {
		t.Errorf("failure not reported: %v", err)
	}

	err = applyClassicBadiLog(&ClassicBadiResult{Program: "ZTEMP_SXCI_1"}, []string{"Job started", "ABAP/4 processor: MESSAGE_TYPE_X", "Job cancelled"})
	if err == nil || !strings.Contains(err.Error(), "Job cancelled") {
		t.Errorf("a cancelled job must fail with its log: %v", err)
	}
}
