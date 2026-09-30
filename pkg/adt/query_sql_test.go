package adt

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The statements below are ones the data preview refused in real use.
func TestNormalizeOpenSQL(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"alias with a period": {
			"SELECT t.lgnum, t.lgtyp FROM /scwm/t301t AS t WHERE t.lgnum = '2000' AND t.spras = 'D'",
			"SELECT t~lgnum, t~lgtyp FROM /scwm/t301t AS t WHERE t~lgnum = '2000' AND t~spras = 'D'",
		},
		"table name with a period": {
			"SELECT E070.TRKORR, E07T.AS4TEXT FROM E070 LEFT OUTER JOIN E07T ON E070.TRKORR = E07T.TRKORR",
			"SELECT E070~TRKORR, E07T~AS4TEXT FROM E070 LEFT OUTER JOIN E07T ON E070~TRKORR = E07T~TRKORR",
		},
		"mixed period and tilde": {
			"SELECT k.trkorr, e.as4user FROM e071k AS k INNER JOIN e070 AS e ON e~trkorr = k~trkorr",
			"SELECT k~trkorr, e~as4user FROM e071k AS k INNER JOIN e070 AS e ON e~trkorr = k~trkorr",
		},
		"DESC in ORDER BY": {
			"SELECT trkorr FROM e070 WHERE as4user = 'X' ORDER BY as4date DESC, trkorr asc",
			"SELECT trkorr FROM e070 WHERE as4user = 'X' ORDER BY as4date DESCENDING, trkorr ASCENDING",
		},
		"closing period": {
			"SELECT * FROM t000.",
			"SELECT * FROM t000",
		},
		"literals are left alone": {
			"SELECT a~x FROM tab AS a WHERE a~txt = 'a.b DESC' AND a~y LIKE 'Z.%'",
			"SELECT a~x FROM tab AS a WHERE a~txt = 'a.b DESC' AND a~y LIKE 'Z.%'",
		},
		"a period after an unknown qualifier stays": {
			"SELECT x FROM tab WHERE y = 1.5",
			"SELECT x FROM tab WHERE y = 1.5",
		},
		"DESC before ORDER BY stays": {
			"SELECT desc FROM tab",
			"SELECT desc FROM tab",
		},
	} {
		got, notes := normalizeOpenSQL(tc.in)
		if got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", name, got, tc.want)
		}
		if (got != tc.in) != (len(notes) > 0) {
			t.Errorf("%s: notes %v do not match the rewrite", name, notes)
		}
	}
}

func TestWrapSQL_BreaksAnINListWithoutBlanks(t *testing.T) {
	var lits []string
	for i := 0; i < 50; i++ {
		lits = append(lits, "'D11K914546'")
	}
	q := "SELECT trkorr FROM e070 WHERE trkorr IN (" + strings.Join(lits, ",") + ")"
	w := wrapSQL(q)
	for n, line := range strings.Split(w, "\n") {
		if len(line) > 200 {
			t.Errorf("line %d is %d characters", n+1, len(line))
		}
	}
	if strings.ReplaceAll(strings.ReplaceAll(w, "\n", ""), " ", "") != strings.ReplaceAll(q, " ", "") {
		t.Error("wrapping changed more than blanks")
	}
}

const unknownColumnXML = `<?xml version="1.0" encoding="utf-8"?><exc:exception xmlns:exc="http://www.sap.com/abapxml/types/communicationframework"><namespace id="http://www.sap.com/adt/wda/dataPreview"/><type id="ExceptionDataPreviewGeneral"/><message lang="EN">Unknown column name &quot;VKBURX&quot;.</message></exc:exception>`

const knvvColumns = `<?xml version="1.0" encoding="utf-8"?><dataPreview:tableData xmlns:dataPreview="http://www.sap.com/adt/dataPreview">
<dataPreview:columns><dataPreview:metadata dataPreview:name="KUNNR"/><dataPreview:dataSet/></dataPreview:columns>
<dataPreview:columns><dataPreview:metadata dataPreview:name="VKORG"/><dataPreview:dataSet/></dataPreview:columns>
<dataPreview:columns><dataPreview:metadata dataPreview:name="VKBUR"/><dataPreview:dataSet/></dataPreview:columns>
</dataPreview:tableData>`

func TestExplainQueryError_UnknownColumnListsTheRealOnes(t *testing.T) {
	c, _ := newTransportTestClient(t, map[string][]string{"/sap/bc/adt/datapreview/ddic": {knvvColumns}})
	c.transport.setCSRFToken("token")
	api := &APIError{StatusCode: 400, Path: "/sap/bc/adt/datapreview/freestyle", Message: unknownColumnXML}
	err := c.explainQueryError(context.Background(), "SELECT kunnr, vkburx FROM knvv", nil, api)
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("got %T %v", err, err)
	}
	if qe.Message != `Unknown column name "VKBURX".` {
		t.Errorf("message %q", qe.Message)
	}
	text := err.Error()
	for _, want := range []string{"KNVV has no column VKBURX", "closest: VKBUR", "KUNNR, VKORG, VKBUR"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing in %s", want, text)
		}
	}
	if !errors.Is(err, api) {
		t.Error("the SAP error is no longer wrapped")
	}
}

func TestExplainQueryError_ClientFieldAndPassThrough(t *testing.T) {
	c, _ := newTransportTestClient(t, nil)
	xml := `<exc:exception><message lang="EN">The client field &quot;MANDT&quot; cannot be specified in the WHERE condition.</message></exc:exception>`
	err := c.explainQueryError(context.Background(), "SELECT * FROM tbd05 WHERE mandt = '100'", nil, &APIError{StatusCode: 400, Message: xml})
	if !strings.Contains(err.Error(), "drop the condition on MANDT") {
		t.Errorf("no hint: %v", err)
	}
	lraw := `<exc:exception><message lang="EN">Only the prefixed length field can be used to read from the LRAW field or LCHR field SDATA.</message></exc:exception>`
	err = c.explainQueryError(context.Background(), "SELECT sdata FROM edid4", nil, &APIError{StatusCode: 400, Message: lraw})
	if !strings.Contains(err.Error(), "such as SDATA cannot") {
		t.Errorf("LRAW hint: %v", err)
	}
	other := errors.New("connection refused")
	if got := c.explainQueryError(context.Background(), "SELECT 1", nil, other); got != other {
		t.Errorf("a non-SAP error was changed: %v", got)
	}
}
