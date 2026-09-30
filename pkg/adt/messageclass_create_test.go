package adt

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestMessageClassCreateBody(t *testing.T) {
	body := buildCreateObjectBody(CreateObjectOptions{
		ObjectType: ObjectTypeMessageClass, Name: "zdemo", Description: "Demo & more",
		PackageName: "$tmp", MasterLanguage: "de",
	}, objectTypes[ObjectTypeMessageClass], "DEVELOPER")
	for _, want := range []string{
		`<mc:messageClass xmlns:mc="http://www.sap.com/adt/MessageClass"`,
		`adtcore:name="ZDEMO"`, `adtcore:type="MSAG/N"`,
		`adtcore:masterLanguage="DE"`, `adtcore:description="Demo &amp; more"`,
		`<adtcore:packageRef adtcore:name="$TMP"/>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s:\n%s", want, body)
		}
	}
	if GetObjectURL(ObjectTypeMessageClass, "ZDEMO", "") != "/sap/bc/adt/messageclass/zdemo" {
		t.Error("object URL")
	}
}

// Without adtcore:language the messages were stored with an empty SPRSL:
// written, reported as written, and never found at runtime.
func TestMessageClassWriteCarriesTheLanguage(t *testing.T) {
	b, err := xml.Marshal(messageClassWrite{XMLNSmc: "m", XMLNSadtcore: "a", Name: "ZDEMO", Language: "DE",
		Messages: []messageWrite{{Number: "001", Text: "Text"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `adtcore:language="DE"`) {
		t.Errorf("no language in %s", b)
	}
}
