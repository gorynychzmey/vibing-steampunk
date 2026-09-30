package adt

import (
	"strings"
	"testing"
)

// A function module in a namespaced group: the group has to be escaped in
// the creation URL and in the container reference, as in every other URL.
func TestFunctionModuleInNamespacedGroup(t *testing.T) {
	opts := CreateObjectOptions{ObjectType: ObjectTypeFunctionMod, Name: "/NS/FM", ParentName: "/NS/GROUP", Description: "d"}
	if got := creationURLFor(opts, objectTypes[ObjectTypeFunctionMod]); got != "/sap/bc/adt/functions/groups/%2Fns%2Fgroup/fmodules" {
		t.Errorf("creation URL %s", got)
	}
	body := buildCreateObjectBody(opts, objectTypes[ObjectTypeFunctionMod], "DEV")
	if !strings.Contains(body, `adtcore:uri="/sap/bc/adt/functions/groups/%2Fns%2Fgroup"`) {
		t.Errorf("container reference not escaped:\n%s", body)
	}
	if got := creationURLFor(CreateObjectOptions{ObjectType: ObjectTypeFunctionMod, ParentName: "ZGROUP"}, objectTypes[ObjectTypeFunctionMod]); got != "/sap/bc/adt/functions/groups/zgroup/fmodules" {
		t.Errorf("plain group %s", got)
	}
}

func TestGroupFromFunctionURI_Namespace(t *testing.T) {
	for uri, want := range map[string]string{
		"/sap/bc/adt/functions/groups/%2fpar%2fvsp_fg/fmodules/%2fpar%2fvsp_fm": "/PAR/VSP_FG",
		"/sap/bc/adt/functions/groups/zdemo/fmodules/z_demo":                    "ZDEMO",
	} {
		if got := groupFromFunctionURI(uri); got != want {
			t.Errorf("%s: %s, want %s", uri, got, want)
		}
	}
}
