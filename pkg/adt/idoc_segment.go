package adt

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// IDoc segments (WE31) have no ADT editor and no remote-enabled API. Their
// function modules (function group EDIJ: SEGMENT_CREATE, SEGMENTDEFINITION_*,
// SEGMENT_DELETE) work without a GUI in a background job, and they record the
// segment in the request themselves: the DDIC structure as R3TR TABL, the
// segment tables as R3TR TDAT EDISEGMENT with its keys. Over ZADT_VSP's
// WebSocket bridge the same calls end in APC_ILLEGAL_STATEMENT (GUI
// personalisation behind the scenes), so they run as a temporary report, like
// the classic BAdI implementations.
//
// A segment's fields live once, in EDSAPPL; a version (EDISDEF) is how many
// of them it has. Released versions therefore fix a prefix of the field list:
// a later version can only add fields at the end. SAP's own editor asks in a
// popup when a released field is renamed or moved; vsp refuses that before
// anything is written. Only one version may be released per SAP release, so a
// change to a segment released in the running release reopens that version
// and releases it again, and a segment released in an older one gets a new
// version.

// SegmentField is one field of a segment.
type SegmentField struct {
	Name        string `json:"name"`
	DataElement string `json:"data_element"`
	// ISOCode marks a field whose value is converted to its ISO code in the
	// IDoc (currency, unit, country and the like).
	ISOCode bool `json:"iso_code,omitempty"`
	// Length is the field's length in the IDoc; SAP derives it from the
	// data element. Only reported, never sent.
	Length int `json:"length,omitempty"`
}

// SegmentVersion is one version of a segment definition (EDISDEF).
type SegmentVersion struct {
	Definition string `json:"definition"` // segment definition name, e.g. Z1DEMO000
	Version    string `json:"version"`
	Released   string `json:"released,omitempty"` // SAP release it was released in
	Closed     bool   `json:"closed"`
	Fields     int    `json:"fields"` // how many of the segment's fields it has
}

// IDocSegment is a segment type as WE31 shows it.
type IDocSegment struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Descriptions map[string]string `json:"descriptions,omitempty"` // by language key
	Qualified    bool              `json:"qualified,omitempty"`
	Package      string            `json:"package,omitempty"`
	Fields       []SegmentField    `json:"fields"`
	Versions     []SegmentVersion  `json:"versions"`
}

// frozenFields is how many leading fields released versions fix.
func (s *IDocSegment) frozenFields() int {
	n := 0
	for _, v := range s.Versions {
		if v.Closed && v.Fields > n {
			n = v.Fields
		}
	}
	return n
}

// SegmentCreate describes a new segment.
type SegmentCreate struct {
	Name        string
	Description string
	Package     string // defaults to $TMP
	Transport   string // required for a transportable package
	Qualified   bool
	Fields      []SegmentField
	Release     bool // release the first version right away
}

// SegmentChange describes a change to a segment.
type SegmentChange struct {
	Name      string
	Transport string
	// Fields is the complete new field list; nil keeps the fields. Fields
	// already in a released version must stay, in their order, with their
	// data elements.
	Fields []SegmentField
	// Release releases the latest version (true) or leaves it open (false).
	// Unset, a version that was released before the change is released
	// again, and an open one stays open.
	Release *bool
}

// SegmentResult is what the generated report reported back.
type SegmentResult struct {
	Name       string   `json:"name"`
	Definition string   `json:"definition,omitempty"`
	Version    string   `json:"version,omitempty"`
	Released   string   `json:"released,omitempty"`
	Closed     bool     `json:"closed"`
	Deleted    bool     `json:"deleted,omitempty"`
	Package    string   `json:"package,omitempty"`
	Transport  string   `json:"transport,omitempty"`
	Task       string   `json:"task,omitempty"`
	Steps      []string `json:"steps,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Program    string   `json:"program"`
}

const segmentMarker = "VSP-SEGM:"

// ReadIDocSegment reads a segment: its header, text, fields and versions.
func (c *Client) ReadIDocSegment(ctx context.Context, name string, run BackgroundRunner) (*IDocSegment, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if err := validateABAPName("segment type", name, 27); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("SEGTYP = '%s'", name)
	head, err := run.ReadTable(ctx, "EDISEGMENT", key, []string{"QUALIFIER"}, 1)
	if err != nil {
		return nil, fmt.Errorf("reading EDISEGMENT: %w", err)
	}
	if len(head) == 0 {
		return nil, fmt.Errorf("segment type %s does not exist", name)
	}
	seg := &IDocSegment{Name: name, Qualified: strings.TrimSpace(head[0]["QUALIFIER"]) == "X"}

	// The text is the segment structure's (DD02T); EDISEGT holds a copy for
	// SAP's own segments and stays empty for one SEGMENT_CREATE made.
	seg.Descriptions = map[string]string{}
	texts, err := run.ReadTable(ctx, "EDISEGT", key, []string{"LANGUA", "DESCRP"}, 0)
	if err != nil {
		return nil, fmt.Errorf("reading EDISEGT: %w", err)
	}
	for _, t := range texts {
		seg.Descriptions[strings.TrimSpace(t["LANGUA"])] = strings.TrimSpace(t["DESCRP"])
	}
	texts, err = run.ReadTable(ctx, "DD02T", fmt.Sprintf("TABNAME = '%s' AND AS4LOCAL = 'A'", name), []string{"DDLANGUAGE", "DDTEXT"}, 0)
	if err != nil {
		return nil, fmt.Errorf("reading DD02T: %w", err)
	}
	for _, t := range texts {
		seg.Descriptions[strings.TrimSpace(t["DDLANGUAGE"])] = strings.TrimSpace(t["DDTEXT"])
	}
	seg.Description = preferredText(seg.Descriptions)

	defs, err := run.ReadTable(ctx, "EDISDEF", key, []string{"VERSION", "SEGDEF", "RELEASED", "CLOSED", "FIELDNUM"}, 0)
	if err != nil {
		return nil, fmt.Errorf("reading EDISDEF: %w", err)
	}
	for _, d := range defs {
		n, _ := strconv.Atoi(strings.TrimSpace(d["FIELDNUM"]))
		seg.Versions = append(seg.Versions, SegmentVersion{
			Definition: strings.TrimSpace(d["SEGDEF"]),
			Version:    strings.TrimSpace(d["VERSION"]),
			Released:   strings.TrimSpace(d["RELEASED"]),
			Closed:     strings.TrimSpace(d["CLOSED"]) == "X",
			Fields:     n,
		})
	}
	sort.Slice(seg.Versions, func(i, j int) bool { return seg.Versions[i].Version < seg.Versions[j].Version })

	fields, err := run.ReadTable(ctx, "EDSAPPL", key, []string{"POS", "FIELDNAME", "ROLLNAME", "EXPLENG", "ISOCODE"}, 0)
	if err != nil {
		return nil, fmt.Errorf("reading EDSAPPL: %w", err)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i]["POS"] < fields[j]["POS"] })
	seg.Fields = []SegmentField{}
	for _, f := range fields {
		n, _ := strconv.Atoi(strings.TrimSpace(f["EXPLENG"]))
		seg.Fields = append(seg.Fields, SegmentField{
			Name:        strings.TrimSpace(f["FIELDNAME"]),
			DataElement: strings.TrimSpace(f["ROLLNAME"]),
			ISOCode:     strings.TrimSpace(f["ISOCODE"]) == "X",
			Length:      n,
		})
	}

	pkg, _, err := objectPackage(ctx, run, "TABL", name)
	if err != nil {
		return nil, err
	}
	seg.Package = pkg
	return seg, nil
}

// preferredText picks the English text, else the German one, else any.
func preferredText(texts map[string]string) string {
	for _, l := range []string{"E", "D"} {
		if t, ok := texts[l]; ok {
			return t
		}
	}
	keys := make([]string, 0, len(texts))
	for k := range texts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return texts[keys[0]]
}

// CreateIDocSegment creates a segment and, with Release, releases it.
func (c *Client) CreateIDocSegment(ctx context.Context, o SegmentCreate, run BackgroundRunner) (*SegmentResult, error) {
	o.Name = strings.ToUpper(strings.TrimSpace(o.Name))
	o.Package = strings.ToUpper(strings.TrimSpace(o.Package))
	o.Transport = strings.ToUpper(strings.TrimSpace(o.Transport))
	if o.Package == "" {
		o.Package = "$TMP"
	}
	o.Fields = normalizedFields(o.Fields)
	if err := validateSegmentCreate(o); err != nil {
		return nil, err
	}
	if err := c.checkMutation(ctx, MutationContext{
		Op: OpCreate, OpName: "CreateIDocSegment",
		Package: o.Package, Transport: o.Transport,
	}); err != nil {
		return nil, err
	}
	rows, err := run.ReadTable(ctx, "EDISEGMENT", fmt.Sprintf("SEGTYP = '%s'", o.Name), []string{"SEGTYP"}, 1)
	if err != nil {
		return nil, fmt.Errorf("reading EDISEGMENT: %w", err)
	}
	if len(rows) > 0 {
		return nil, fmt.Errorf("segment type %s already exists", o.Name)
	}
	res := &SegmentResult{Name: o.Name, Package: o.Package, Transport: o.Transport}
	return c.runSegmentReport(ctx, res, segmentReport{
		Op: "CREATE", Name: o.Name, Description: o.Description, Package: o.Package,
		Transport: o.Transport, Qualified: o.Qualified, Fields: o.Fields,
		SetFields: true, Release: releaseFlag(&o.Release),
	}, run)
}

// ChangeIDocSegment changes a segment's fields and/or releases it.
func (c *Client) ChangeIDocSegment(ctx context.Context, o SegmentChange, run BackgroundRunner) (*SegmentResult, error) {
	o.Name = strings.ToUpper(strings.TrimSpace(o.Name))
	o.Transport = strings.ToUpper(strings.TrimSpace(o.Transport))
	seg, err := c.ReadIDocSegment(ctx, o.Name, run)
	if err != nil {
		return nil, err
	}
	if len(seg.Versions) == 0 {
		return nil, fmt.Errorf("segment type %s has no definition (EDISDEF)", o.Name)
	}
	latest := seg.Versions[len(seg.Versions)-1]
	setFields := false
	if o.Fields != nil {
		o.Fields = normalizedFields(o.Fields)
		if err := validateSegmentFields(o.Fields); err != nil {
			return nil, err
		}
		if err := checkReleasedFieldsKept(seg, o.Fields); err != nil {
			return nil, err
		}
		setFields = !sameFields(seg.Fields, o.Fields)
	}
	if !setFields && (o.Release == nil || *o.Release == latest.Closed) {
		if o.Fields == nil {
			return nil, fmt.Errorf("nothing to change: version %s of %s is already %s", latest.Version, o.Name, openOrReleased(latest.Closed))
		}
		return nil, fmt.Errorf("nothing to change: %s has these fields, and its version %s is %s", o.Name, latest.Version, openOrReleased(latest.Closed))
	}
	if err := checkSegmentTransport(o.Name, seg.Package, o.Transport); err != nil {
		return nil, err
	}
	if err := c.checkMutation(ctx, MutationContext{
		Op: OpUpdate, OpName: "ChangeIDocSegment",
		Package: seg.Package, Transport: o.Transport,
	}); err != nil {
		return nil, err
	}
	res := &SegmentResult{Name: o.Name, Package: seg.Package, Transport: o.Transport}
	return c.runSegmentReport(ctx, res, segmentReport{
		Op: "CHANGE", Name: o.Name, Package: seg.Package, Transport: o.Transport,
		Fields: o.Fields, SetFields: setFields, Release: releaseFlag(o.Release),
	}, run)
}

// DeleteIDocSegment deletes a segment with all its versions. A released
// latest version is reopened first, as WE31 does.
func (c *Client) DeleteIDocSegment(ctx context.Context, name, transport string, run BackgroundRunner) (*SegmentResult, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	transport = strings.ToUpper(strings.TrimSpace(transport))
	seg, err := c.ReadIDocSegment(ctx, name, run)
	if err != nil {
		return nil, err
	}
	if err := checkSegmentTransport(name, seg.Package, transport); err != nil {
		return nil, err
	}
	if err := c.checkMutation(ctx, MutationContext{
		Op: OpDelete, OpName: "DeleteIDocSegment",
		Package: seg.Package, Transport: transport,
	}); err != nil {
		return nil, err
	}
	res := &SegmentResult{Name: name, Package: seg.Package, Transport: transport}
	return c.runSegmentReport(ctx, res, segmentReport{
		Op: "DELETE", Name: name, Package: seg.Package, Transport: transport,
	}, run)
}

func (c *Client) runSegmentReport(ctx context.Context, res *SegmentResult, r segmentReport, run BackgroundRunner) (*SegmentResult, error) {
	tr, err := c.runTempReport(ctx, "ZTEMP_SEGM_", func(prog string) string {
		return segmentReportSource(prog, r)
	}, run)
	res.Program = tr.Program
	res.Warnings = append(res.Warnings, tr.Warnings...)
	if err != nil {
		return res, err
	}
	if err := applySegmentLog(res, tr.Lines); err != nil {
		// The modules need each step committed before the next, so the
		// steps that ran before the failure stay done; say which.
		if len(res.Steps) > 0 {
			err = fmt.Errorf("%w (done before the failure, each committed: %s)", err, strings.Join(res.Steps, ", "))
		}
		return res, err
	}
	return res, nil
}

func checkSegmentTransport(name, pkg, transport string) error {
	if transport != "" {
		if err := validateABAPName("transport", transport, 20); err != nil {
			return err
		}
	}
	if pkg != "" && !strings.HasPrefix(pkg, "$") && transport == "" {
		return fmt.Errorf("%s is in the transportable package %s: a transport request is required", name, pkg)
	}
	return nil
}

func openOrReleased(closed bool) string {
	if closed {
		return "released"
	}
	return "open"
}

// releaseFlag is the report's release switch: X release, - leave open,
// blank as before.
func releaseFlag(b *bool) string {
	switch {
	case b == nil:
		return " "
	case *b:
		return "X"
	default:
		return "-"
	}
}

func normalizedFields(fields []SegmentField) []SegmentField {
	out := make([]SegmentField, 0, len(fields))
	for _, f := range fields {
		out = append(out, SegmentField{
			Name:        strings.ToUpper(strings.TrimSpace(f.Name)),
			DataElement: strings.ToUpper(strings.TrimSpace(f.DataElement)),
			ISOCode:     f.ISOCode,
		})
	}
	return out
}

func sameFields(a, b []SegmentField) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].DataElement != b[i].DataElement || a[i].ISOCode != b[i].ISOCode {
			return false
		}
	}
	return true
}

// checkReleasedFieldsKept refuses a field list that drops, renames, moves or
// retypes a field a released version has. SAP would ask about that in a
// popup; in a background job there is nobody to answer.
func checkReleasedFieldsKept(seg *IDocSegment, fields []SegmentField) error {
	frozen := seg.frozenFields()
	if frozen > len(seg.Fields) {
		frozen = len(seg.Fields)
	}
	if len(fields) < frozen {
		return fmt.Errorf("%s has %d fields in a released version; the new list has only %d. Released fields can only be added to, at the end", seg.Name, frozen, len(fields))
	}
	for i := 0; i < frozen; i++ {
		was, now := seg.Fields[i], fields[i]
		if was.Name != now.Name || was.DataElement != now.DataElement || was.ISOCode != now.ISOCode {
			return fmt.Errorf("field %d of %s is %s (%s) in a released version and would become %s (%s). Released fields can only be added to, at the end",
				i+1, seg.Name, was.Name, was.DataElement, now.Name, now.DataElement)
		}
	}
	return nil
}

func validateSegmentCreate(o SegmentCreate) error {
	if err := validateABAPName("segment type", o.Name, 27); err != nil {
		return err
	}
	if err := validateABAPName("package", o.Package, 30); err != nil {
		return err
	}
	if err := checkSegmentTransport(o.Name, o.Package, o.Transport); err != nil {
		return err
	}
	if o.Description == "" {
		return fmt.Errorf("a description is required")
	}
	if len([]rune(o.Description)) > 60 {
		return fmt.Errorf("the description has %d characters; EDISEGT holds 60", len([]rune(o.Description)))
	}
	if strings.ContainsAny(o.Description, "\r\n") {
		return fmt.Errorf("line breaks are not allowed in the description")
	}
	if len(o.Fields) == 0 {
		return fmt.Errorf("a segment needs at least one field")
	}
	return validateSegmentFields(o.Fields)
}

func validateSegmentFields(fields []SegmentField) error {
	seen := map[string]bool{}
	for i, f := range fields {
		if err := validateABAPName(fmt.Sprintf("name of field %d", i+1), f.Name, 30); err != nil {
			return err
		}
		if err := validateABAPName(fmt.Sprintf("data element of field %s", f.Name), f.DataElement, 30); err != nil {
			return err
		}
		if seen[f.Name] {
			return fmt.Errorf("field %s is named twice", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}

// applySegmentLog reads the markers the generated report wrote into its job
// log. A report that ended without VSP-SEGM:OK failed, whether it said why or
// was cancelled before it could.
func applySegmentLog(res *SegmentResult, lines []string) error {
	done := false
	var failure string
	for _, line := range lines {
		i := strings.Index(line, segmentMarker)
		if i < 0 {
			continue
		}
		key, value, _ := strings.Cut(strings.TrimSpace(line[i+len(segmentMarker):]), "=")
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "OK":
			done = true
		case "ERR":
			failure = value
		case "WARN":
			res.Warnings = append(res.Warnings, value)
		case "STEP":
			res.Steps = append(res.Steps, value)
		case "DEF":
			res.Definition = value
		case "VERSION":
			res.Version = value
		case "RELEASED":
			res.Released = value
		case "CLOSED":
			res.Closed = value == "X"
		case "TASK":
			res.Task = value
		case "DELETED":
			res.Deleted = value == "X"
		}
	}
	if failure != "" {
		return fmt.Errorf("%s", failure)
	}
	if !done {
		tail := lines
		if len(tail) > 5 {
			tail = tail[len(tail)-5:]
		}
		return fmt.Errorf("the report %s ended without reporting success; job log: %s", res.Program, strings.Join(tail, " | "))
	}
	return nil
}

// segmentReport is what the generated report is to do.
type segmentReport struct {
	Op          string // CREATE, CHANGE, DELETE
	Name        string
	Description string
	Package     string
	Transport   string
	Qualified   bool
	Fields      []SegmentField
	SetFields   bool
	Release     string // X, -, or blank: see releaseFlag
}

// segmentReportSource is the report behind all three operations. Nothing is
// committed before a step succeeds; a failing step rolls back and ends the
// report with VSP-SEGM:ERR.
func segmentReportSource(prog string, r segmentReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REPORT %s.\n* Generated by vsp: %s IDoc segment %s. Deleted after the run.\n", strings.ToLower(prog), strings.ToLower(r.Op), r.Name)
	fmt.Fprintf(&b, "CONSTANTS: gc_op TYPE c LENGTH 6 VALUE %s,\n", abapLiteral(r.Op))
	fmt.Fprintf(&b, "           gc_seg TYPE edisegmhd-segtyp VALUE %s,\n", abapLiteral(r.Name))
	fmt.Fprintf(&b, "           gc_set_fields TYPE c VALUE %s,\n", abapBool(r.SetFields))
	fmt.Fprintf(&b, "           gc_release TYPE c VALUE %s.\n", abapLiteral(r.Release))
	b.WriteString(`DATA: gt_stru TYPE STANDARD TABLE OF edisegstru,
      gs_stru TYPE edisegstru,
      gs_def TYPE edisegmdef,
      gs_hd TYPE edisegmhd,
      gs_sdef TYPE edisdef,
      gv_task TYPE e070-trkorr,
      gv_order TYPE e070-trkorr,
      gv_pkg TYPE devclass,
      gv_res TYPE sy-subrc,
      gv_was_closed TYPE c,
      gv_reopened TYPE c.

START-OF-SELECTION.
`)
	fmt.Fprintf(&b, "  gv_order = %s.\n  gv_pkg = %s.\n", abapLiteral(r.Transport), abapLiteral(r.Package))
	for i, f := range r.Fields {
		fmt.Fprintf(&b, "  CLEAR gs_stru.\n  gs_stru-pos = %d.\n  gs_stru-fieldname = %s.\n  gs_stru-rollname = %s.\n  gs_stru-isocode = %s.\n  APPEND gs_stru TO gt_stru.\n",
			i+1, abapLiteral(f.Name), abapLiteral(f.DataElement), abapBool(f.ISOCode))
	}
	if r.Op == "CREATE" {
		fmt.Fprintf(&b, "  gs_hd-descrp = %s.\n  gs_hd-qualifier = %s.\n", abapLiteral(r.Description), abapBool(r.Qualified))
	}
	b.WriteString(`  CASE gc_op.
    WHEN 'CREATE'.
      gs_hd-segtyp = gc_seg.
      CALL FUNCTION 'SEGMENT_CREATE'
        IMPORTING segmentdefinition = gs_def
                  task = gv_task
        TABLES segmentstructure = gt_stru
        CHANGING segmentheader = gs_hd
                 devclass = gv_pkg
                 order = gv_order
        EXCEPTIONS OTHERS = 1.
      IF sy-subrc <> 0.
        PERFORM fail USING 'SEGMENT_CREATE'.
      ENDIF.
      PERFORM step USING 'CREATE' gs_def-segdef.
      IF gc_release = 'X'.
        PERFORM close.
      ENDIF.
    WHEN 'CHANGE'.
      PERFORM latest.
      IF gc_set_fields = 'X'.
        IF gs_sdef-closed = 'X'.
          gv_was_closed = 'X'.
          IF gs_sdef-released = sy-saprl.
            PERFORM unclose.
            PERFORM modify.
          ELSE.
            CALL FUNCTION 'SEGMENTDEFINITION_APPEND'
              EXPORTING segmenttyp = gc_seg
              IMPORTING segmentdefinition = gs_def
                        task = gv_task
              TABLES segmentstructure = gt_stru
              CHANGING order = gv_order
              EXCEPTIONS OTHERS = 1.
            IF sy-subrc <> 0.
              PERFORM fail USING 'SEGMENTDEFINITION_APPEND'.
            ENDIF.
            PERFORM step USING 'APPEND' gs_def-segdef.
          ENDIF.
        ELSE.
          PERFORM modify.
        ENDIF.
      ENDIF.
      PERFORM latest.
      IF gs_sdef-closed IS INITIAL
         AND ( gc_release = 'X' OR ( gc_release = ' ' AND gv_was_closed = 'X' ) ).
        PERFORM close.
      ELSEIF gs_sdef-closed = 'X' AND gc_release = '-'.
        PERFORM unclose.
      ENDIF.
    WHEN 'DELETE'.
      PERFORM latest.
      IF gs_sdef-closed = 'X'.
        PERFORM unclose.
      ENDIF.
      CALL FUNCTION 'SEGMENT_DELETE'
        EXPORTING segmenttyp = gc_seg
        IMPORTING result = gv_res
                  task = gv_task
        CHANGING order = gv_order
        EXCEPTIONS OTHERS = 1.
      IF sy-subrc <> 0 OR gv_res <> 0.
        PERFORM fail USING 'SEGMENT_DELETE'.
      ENDIF.
      COMMIT WORK AND WAIT.
      PERFORM step USING 'DELETE' gc_seg.
      PERFORM say USING 'DELETED' 'X'.
      PERFORM say USING 'OK' 'X'.
      RETURN.
  ENDCASE.
  PERFORM latest.
  PERFORM say USING 'DEF' gs_sdef-segdef.
  PERFORM say USING 'VERSION' gs_sdef-version.
  PERFORM say USING 'CLOSED' gs_sdef-closed.
  PERFORM say USING 'RELEASED' gs_sdef-released.
  PERFORM say USING 'OK' 'X'.

FORM latest.
  DATA lt_sdef TYPE STANDARD TABLE OF edisdef.
  CLEAR gs_sdef.
  SELECT * FROM edisdef INTO TABLE lt_sdef WHERE segtyp = gc_seg.
  SORT lt_sdef BY version DESCENDING.
  READ TABLE lt_sdef INTO gs_sdef INDEX 1.
  IF sy-subrc <> 0.
    PERFORM fail_text USING 'EDISDEF' 'the segment has no definition'.
  ENDIF.
ENDFORM.

FORM modify.
  PERFORM latest.
  CLEAR gs_def.
  MOVE-CORRESPONDING gs_sdef TO gs_def.
  CALL FUNCTION 'SEGMENTDEFINITION_MODIFY'
    IMPORTING task = gv_task
    TABLES segmentstructure = gt_stru
    CHANGING segmentdefinition = gs_def
             order = gv_order
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'SEGMENTDEFINITION_MODIFY'.
  ENDIF.
  PERFORM step USING 'MODIFY' gs_sdef-segdef.
ENDFORM.

FORM unclose.
  CALL FUNCTION 'SEGMENTDEFINITION_UNCLOSE'
    EXPORTING segmenttyp = gc_seg
    IMPORTING segmentdefinition = gs_def
              task = gv_task
    CHANGING order = gv_order
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'SEGMENTDEFINITION_UNCLOSE'.
  ENDIF.
  PERFORM step USING 'UNCLOSE' gs_def-segdef.
  gv_reopened = 'X'.
ENDFORM.

FORM close.
  CALL FUNCTION 'SEGMENTDEFINITION_CLOSE'
    EXPORTING segmenttyp = gc_seg
    IMPORTING segmentdefinition = gs_def
              task = gv_task
    CHANGING order = gv_order
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'SEGMENTDEFINITION_CLOSE'.
  ENDIF.
  PERFORM step USING 'CLOSE' gs_def-segdef.
  CLEAR gv_reopened.
ENDFORM.

* Every step is committed on its own: the next one reads what the last one
* wrote, as WE31 does between its screens.
FORM step USING iv_step TYPE csequence iv_what TYPE csequence.
  DATA lv_text TYPE string.
  COMMIT WORK AND WAIT.
  CONCATENATE iv_step iv_what INTO lv_text SEPARATED BY space.
  PERFORM say USING 'STEP' lv_text.
  IF gv_task IS NOT INITIAL.
    PERFORM say USING 'TASK' gv_task.
  ENDIF.
ENDFORM.

FORM say USING iv_key TYPE csequence iv_value TYPE clike.
  DATA lv_text TYPE string.
  CONCATENATE 'VSP-SEGM:' iv_key '=' iv_value INTO lv_text.
  MESSAGE lv_text TYPE 'S'.
ENDFORM.

FORM fail USING iv_step TYPE csequence.
  DATA: lv_msg  TYPE string,
        lv_code TYPE string.
  IF sy-msgid IS INITIAL.
    lv_code = sy-subrc.
    CONCATENATE 'sy-subrc' lv_code INTO lv_msg SEPARATED BY space.
  ELSE.
    MESSAGE ID sy-msgid TYPE 'S' NUMBER sy-msgno
            WITH sy-msgv1 sy-msgv2 sy-msgv3 sy-msgv4 INTO lv_msg.
    lv_msg = |{ lv_msg } ({ sy-msgid } { sy-msgno })|.
  ENDIF.
  PERFORM fail_text USING iv_step lv_msg.
ENDFORM.

FORM fail_text USING iv_step TYPE csequence iv_text TYPE csequence.
  DATA lv_msg TYPE string.
  lv_msg = |{ iv_step }: { iv_text }|.
  ROLLBACK WORK.
  PERFORM say USING 'ERR' lv_msg.
* Each step is committed, so a failure after UNCLOSE would leave a released
* segment open. Release it again, as it was before this run.
  IF gv_reopened = 'X'.
    CLEAR gv_reopened.
    CALL FUNCTION 'SEGMENTDEFINITION_CLOSE'
      EXPORTING segmenttyp = gc_seg
      IMPORTING segmentdefinition = gs_def
                task = gv_task
      CHANGING order = gv_order
      EXCEPTIONS OTHERS = 1.
    IF sy-subrc = 0.
      COMMIT WORK AND WAIT.
      PERFORM say USING 'WARN' 'The segment was reopened for this change and is released again, as before.'.
    ELSE.
      ROLLBACK WORK.
      PERFORM say USING 'WARN' 'The segment was reopened for this change and could not be released again; it is open now.'.
    ENDIF.
  ENDIF.
  LEAVE PROGRAM.
ENDFORM.
`)
	return b.String()
}
