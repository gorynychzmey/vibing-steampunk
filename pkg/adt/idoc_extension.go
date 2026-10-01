package adt

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// IDoc extensions (WE30, object type IEXT) add segments of one's own to a
// basic type. Like segments they have no ADT editor and no remote-enabled
// API; the function modules of group EDIM do the work in a background job
// and record the extension in the request themselves (R3TR IEXT). They run
// as a temporary report, like the segments and the classic BAdIs.
//
// An extension stores only what it adds (CIMSYN): each added segment, the
// basic-type segment it hangs below (or, nested, the added segment it hangs
// below), and how often it may occur.
//
// Two things WE30 does differently from its modules:
//
//   - EXTTYPE_UPDATE does not look at the release flag: a released extension
//     takes changes directly. Only the editor refuses them until the release
//     is cancelled. vsp lets a released extension take additions -- what was
//     released stays as it is -- which ends in the state WE30 reaches by
//     cancelling, changing and releasing again.
//   - EXTTYPE_UNCLOSE asks for confirmation (note 844899) whenever the
//     release and component release it compares differ, and for an extension
//     in a customer package they always do: the existence check it reads
//     them from returns no package. In a background job nobody answers, and
//     the release stays. vsp therefore runs the steps that follow the
//     confirmation -- authorization, existence, no view on the extension,
//     lock, transport entry, release flags reset, unlock -- and only for an
//     extension released in the running SAP release; one released in an
//     earlier release is refused, as the warning is meant for that case.

// ExtensionSegment is one segment an extension adds.
type ExtensionSegment struct {
	Segment string `json:"segment"`
	// Parent is the segment it hangs below: a segment of the basic type, or
	// a segment this extension adds before it.
	Parent    string `json:"parent"`
	Min       int64  `json:"min"`
	Max       int64  `json:"max"`
	Mandatory bool   `json:"mandatory,omitempty"`
}

// IDocExtension is an extension type as WE30 shows it.
type IDocExtension struct {
	Name         string             `json:"name"`
	BasicType    string             `json:"basic_type"`
	Description  string             `json:"description,omitempty"`
	Descriptions map[string]string  `json:"descriptions,omitempty"`
	Package      string             `json:"package,omitempty"`
	Released     string             `json:"released,omitempty"`
	Closed       bool               `json:"closed"`
	Segments     []ExtensionSegment `json:"segments"`
}

// ExtensionCreate describes a new extension.
type ExtensionCreate struct {
	Name        string
	BasicType   string
	Description string
	Package     string // defaults to $TMP
	Transport   string
	Segments    []ExtensionSegment
	Release     bool
}

// ExtensionChange describes a change to an extension.
type ExtensionChange struct {
	Name        string
	Transport   string
	Description string // empty keeps it
	// Segments is the complete new list; nil keeps them. A released
	// extension keeps every segment it has, unchanged; new ones are added.
	Segments []ExtensionSegment
	// Release releases (true) or cancels the release (false); unset leaves
	// the release state as it is.
	Release *bool
}

// ExtensionResult is what the generated report reported back.
type ExtensionResult struct {
	Name      string   `json:"name"`
	Released  string   `json:"released,omitempty"`
	Closed    bool     `json:"closed"`
	Deleted   bool     `json:"deleted,omitempty"`
	Package   string   `json:"package,omitempty"`
	Transport string   `json:"transport,omitempty"`
	Steps     []string `json:"steps,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Program   string   `json:"program"`
}

const extensionMarker = "VSP-IEXT:"

// ReadIDocExtension reads an extension: header, text, added segments.
func (c *Client) ReadIDocExtension(ctx context.Context, name string, run BackgroundRunner) (*IDocExtension, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if err := validateABAPName("extension", name, 30); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("CIMTYP = '%s'", name)
	head, err := run.ReadTable(ctx, "EDCIM", key, []string{"IDOCTYP", "CLOSED", "RELEASED"}, 1)
	if err != nil {
		return nil, fmt.Errorf("reading EDCIM: %w", err)
	}
	if len(head) == 0 {
		return nil, fmt.Errorf("extension %s does not exist", name)
	}
	ext := &IDocExtension{
		Name:      name,
		BasicType: strings.TrimSpace(head[0]["IDOCTYP"]),
		Closed:    strings.TrimSpace(head[0]["CLOSED"]) == "X",
		Released:  strings.TrimSpace(head[0]["RELEASED"]),
	}
	texts, err := run.ReadTable(ctx, "EDCIMT", key, []string{"LANGUA", "DESCRP"}, 0)
	if err != nil {
		return nil, fmt.Errorf("reading EDCIMT: %w", err)
	}
	ext.Descriptions = map[string]string{}
	for _, t := range texts {
		ext.Descriptions[strings.TrimSpace(t["LANGUA"])] = strings.TrimSpace(t["DESCRP"])
	}
	ext.Description = preferredText(ext.Descriptions)

	rows, err := run.ReadTable(ctx, "CIMSYN", key, []string{"NR", "SEGTYP", "CIMSTP", "PARSEG", "MUSTFL", "OCCMIN", "OCCMAX"}, 0)
	if err != nil {
		return nil, fmt.Errorf("reading CIMSYN: %w", err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["NR"] < rows[j]["NR"] })
	ext.Segments = []ExtensionSegment{}
	for _, r := range rows {
		parent := strings.TrimSpace(r["PARSEG"])
		if parent == "" {
			parent = strings.TrimSpace(r["SEGTYP"])
		}
		minOcc, _ := strconv.ParseInt(strings.TrimSpace(r["OCCMIN"]), 10, 64)
		maxOcc, _ := strconv.ParseInt(strings.TrimSpace(r["OCCMAX"]), 10, 64)
		ext.Segments = append(ext.Segments, ExtensionSegment{
			Segment:   strings.TrimSpace(r["CIMSTP"]),
			Parent:    parent,
			Min:       minOcc,
			Max:       maxOcc,
			Mandatory: strings.TrimSpace(r["MUSTFL"]) == "X",
		})
	}
	pkg, _, err := objectPackage(ctx, run, "IEXT", name)
	if err != nil {
		return nil, err
	}
	ext.Package = pkg
	return ext, nil
}

// CreateIDocExtension creates an extension and, with Release, releases it.
func (c *Client) CreateIDocExtension(ctx context.Context, o ExtensionCreate, run BackgroundRunner) (*ExtensionResult, error) {
	o.Name = strings.ToUpper(strings.TrimSpace(o.Name))
	o.BasicType = strings.ToUpper(strings.TrimSpace(o.BasicType))
	o.Package = strings.ToUpper(strings.TrimSpace(o.Package))
	o.Transport = strings.ToUpper(strings.TrimSpace(o.Transport))
	if o.Package == "" {
		o.Package = "$TMP"
	}
	o.Segments = normalizedExtensionSegments(o.Segments)
	if err := validateExtensionCreate(o); err != nil {
		return nil, err
	}
	rows, err := extensionSyntax(o.Segments)
	if err != nil {
		return nil, err
	}
	if err = c.checkMutation(ctx, MutationContext{
		Op: OpCreate, OpName: "CreateIDocExtension",
		Package: o.Package, Transport: o.Transport,
	}); err != nil {
		return nil, err
	}
	exists, err := run.ReadTable(ctx, "EDCIM", fmt.Sprintf("CIMTYP = '%s'", o.Name), []string{"CIMTYP"}, 1)
	if err != nil {
		return nil, fmt.Errorf("reading EDCIM: %w", err)
	}
	if len(exists) > 0 {
		return nil, fmt.Errorf("extension %s already exists", o.Name)
	}
	res := &ExtensionResult{Name: o.Name, Package: o.Package, Transport: o.Transport}
	return c.runExtensionReport(ctx, res, extensionReport{
		Op: "CREATE", Name: o.Name, BasicType: o.BasicType, Description: o.Description,
		Package: o.Package, Transport: o.Transport, Syntax: rows, SetSyntax: true,
		Release: releaseFlag(&o.Release),
	}, run)
}

// ChangeIDocExtension changes an extension's segments or description, and
// releases it or cancels its release.
func (c *Client) ChangeIDocExtension(ctx context.Context, o ExtensionChange, run BackgroundRunner) (*ExtensionResult, error) {
	o.Name = strings.ToUpper(strings.TrimSpace(o.Name))
	o.Transport = strings.ToUpper(strings.TrimSpace(o.Transport))
	ext, err := c.ReadIDocExtension(ctx, o.Name, run)
	if err != nil {
		return nil, err
	}
	r := extensionReport{Op: "CHANGE", Name: o.Name, BasicType: ext.BasicType, Package: ext.Package, Transport: o.Transport, Release: releaseFlag(o.Release), Released: ext.Released}
	changes := false
	if o.Segments != nil || (o.Description != "" && o.Description != ext.Description) {
		segments := ext.Segments
		if o.Segments != nil {
			segments = normalizedExtensionSegments(o.Segments)
			if err = validateExtensionSegments(segments); err != nil {
				return nil, err
			}
			if ext.Closed {
				if err = checkReleasedSegmentsKept(ext, segments); err != nil {
					return nil, err
				}
			}
		}
		description := ext.Description
		if o.Description != "" {
			if err = validateDescription(o.Description); err != nil {
				return nil, err
			}
			description = o.Description
		}
		if !sameExtensionSegments(ext.Segments, segments) || description != ext.Description {
			if r.Syntax, err = extensionSyntax(segments); err != nil {
				return nil, err
			}
			r.Description, r.SetSyntax, changes = description, true, true
		}
	}
	if !changes && (o.Release == nil || *o.Release == ext.Closed) {
		return nil, fmt.Errorf("nothing to change: %s has these segments and is %s", o.Name, openOrReleased(ext.Closed))
	}
	if ext.Closed && (changes || (o.Release != nil && !*o.Release)) {
		if err := c.checkCurrentRelease(ctx, run, ext); err != nil {
			return nil, err
		}
	}
	if err := checkSegmentTransport(o.Name, ext.Package, o.Transport); err != nil {
		return nil, err
	}
	if err := c.checkMutation(ctx, MutationContext{
		Op: OpUpdate, OpName: "ChangeIDocExtension",
		Package: ext.Package, Transport: o.Transport,
	}); err != nil {
		return nil, err
	}
	res := &ExtensionResult{Name: o.Name, Package: ext.Package, Transport: o.Transport}
	return c.runExtensionReport(ctx, res, r, run)
}

// DeleteIDocExtension deletes an extension; a released one has its release
// cancelled first.
func (c *Client) DeleteIDocExtension(ctx context.Context, name, transport string, run BackgroundRunner) (*ExtensionResult, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	transport = strings.ToUpper(strings.TrimSpace(transport))
	ext, err := c.ReadIDocExtension(ctx, name, run)
	if err != nil {
		return nil, err
	}
	if ext.Closed {
		if err := c.checkCurrentRelease(ctx, run, ext); err != nil {
			return nil, err
		}
	}
	if err := checkSegmentTransport(name, ext.Package, transport); err != nil {
		return nil, err
	}
	if err := c.checkMutation(ctx, MutationContext{
		Op: OpDelete, OpName: "DeleteIDocExtension",
		Package: ext.Package, Transport: transport,
	}); err != nil {
		return nil, err
	}
	res := &ExtensionResult{Name: name, Package: ext.Package, Transport: transport}
	return c.runExtensionReport(ctx, res, extensionReport{
		Op: "DELETE", Name: name, Package: ext.Package, Transport: transport, Released: ext.Released,
	}, run)
}

// checkCurrentRelease refuses to touch an extension released in an earlier
// SAP release: cancelling such a release is what SAP's warning (note 844899)
// is for, and a background job cannot answer it.
func (c *Client) checkCurrentRelease(ctx context.Context, run BackgroundRunner, ext *IDocExtension) error {
	rows, err := run.ReadTable(ctx, "CVERS", "COMPONENT = 'SAP_BASIS'", []string{"RELEASE"}, 1)
	if err != nil {
		return fmt.Errorf("reading the SAP release: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("cannot tell the SAP release (CVERS has no SAP_BASIS)")
	}
	current := strings.TrimSpace(rows[0]["RELEASE"])
	if ext.Released != current {
		return fmt.Errorf("%s was released in SAP release %s, this system runs %s: cancelling that release is what WE30 warns about (note 844899), so vsp does not; create a successor extension instead", ext.Name, ext.Released, current)
	}
	return nil
}

func (c *Client) runExtensionReport(ctx context.Context, res *ExtensionResult, r extensionReport, run BackgroundRunner) (*ExtensionResult, error) {
	tr, err := c.runTempReport(ctx, "ZTEMP_IEXT_", func(prog string) string {
		return extensionReportSource(prog, r)
	}, run)
	res.Program = tr.Program
	res.Warnings = append(res.Warnings, tr.Warnings...)
	if err != nil {
		return res, err
	}
	if err := applyExtensionLog(res, tr.Lines); err != nil {
		if len(res.Steps) > 0 {
			err = fmt.Errorf("%w (done before the failure, each committed: %s)", err, strings.Join(res.Steps, ", "))
		}
		return res, err
	}
	return res, nil
}

func normalizedExtensionSegments(segs []ExtensionSegment) []ExtensionSegment {
	out := make([]ExtensionSegment, 0, len(segs))
	for _, s := range segs {
		s.Segment = strings.ToUpper(strings.TrimSpace(s.Segment))
		s.Parent = strings.ToUpper(strings.TrimSpace(s.Parent))
		if s.Min == 0 {
			s.Min = 1
		}
		if s.Max == 0 {
			s.Max = 1
		}
		out = append(out, s)
	}
	return out
}

func sameExtensionSegments(a, b []ExtensionSegment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkReleasedSegmentsKept refuses a list that drops or changes a segment a
// released extension has: partner systems already know it as it is.
func checkReleasedSegmentsKept(ext *IDocExtension, segs []ExtensionSegment) error {
	have := map[string]ExtensionSegment{}
	for _, s := range segs {
		have[s.Segment] = s
	}
	for _, old := range ext.Segments {
		now, ok := have[old.Segment]
		if !ok {
			return fmt.Errorf("%s is released and has segment %s; a released extension keeps its segments, new ones can only be added", ext.Name, old.Segment)
		}
		if now != old {
			return fmt.Errorf("%s is released and has segment %s below %s (%d..%d); a released extension keeps its segments as they are, new ones can only be added", ext.Name, old.Segment, old.Parent, old.Min, old.Max)
		}
	}
	return nil
}

func validateDescription(d string) error {
	if len([]rune(d)) > 60 {
		return fmt.Errorf("the description has %d characters; 60 fit", len([]rune(d)))
	}
	if strings.ContainsAny(d, "\r\n") {
		return fmt.Errorf("line breaks are not allowed in the description")
	}
	return nil
}

func validateExtensionCreate(o ExtensionCreate) error {
	if err := validateABAPName("extension", o.Name, 30); err != nil {
		return err
	}
	if err := validateABAPName("basic type", o.BasicType, 30); err != nil {
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
	if err := validateDescription(o.Description); err != nil {
		return err
	}
	if len(o.Segments) == 0 {
		return fmt.Errorf("an extension needs at least one segment")
	}
	return validateExtensionSegments(o.Segments)
}

func validateExtensionSegments(segs []ExtensionSegment) error {
	seen := map[string]bool{}
	for i, s := range segs {
		if err := validateABAPName(fmt.Sprintf("segment %d", i+1), s.Segment, 27); err != nil {
			return err
		}
		if err := validateABAPName(fmt.Sprintf("parent of %s", s.Segment), s.Parent, 27); err != nil {
			return err
		}
		if seen[s.Segment] {
			return fmt.Errorf("segment %s is added twice", s.Segment)
		}
		seen[s.Segment] = true
		if s.Min < 1 || s.Max < s.Min || s.Max > 9999999999 {
			return fmt.Errorf("segment %s: min %d and max %d must satisfy 1 <= min <= max", s.Segment, s.Min, s.Max)
		}
	}
	return nil
}

// extensionRow is one EDI_IAPI03 row.
type extensionRow struct {
	Ref, Segment, Parent   string
	Nr, ParentNr, Level    int
	Min, Max               int64
	Mandatory, HasChildren bool
}

// extensionSyntax turns the segment list into EDI_IAPI03 rows. A segment
// below a basic-type segment refers to it and sits at level 1, or 2 when it
// may repeat; one below an added segment names that parent, refers to the
// same basic-type segment, and sits one level deeper. A parent must come
// before its children.
func extensionSyntax(segs []ExtensionSegment) ([]extensionRow, error) {
	rows := make([]extensionRow, 0, len(segs))
	index := map[string]int{}
	for i, s := range segs {
		row := extensionRow{Segment: s.Segment, Nr: i + 1, Min: s.Min, Max: s.Max, Mandatory: s.Mandatory}
		if p, ok := index[s.Parent]; ok {
			row.Ref, row.Parent, row.ParentNr, row.Level = rows[p].Ref, s.Parent, rows[p].Nr, rows[p].Level+1
			rows[p].HasChildren = true
		} else {
			for _, later := range segs[i+1:] {
				if later.Segment == s.Parent {
					return nil, fmt.Errorf("segment %s is listed before its parent %s; list a parent first", s.Segment, s.Parent)
				}
			}
			// EXTTYPE_INTEGRITY_CHECK's rule: a segment hanging below the
			// basic type is level 2 when it may repeat, 1 otherwise.
			row.Ref, row.Level = s.Parent, 1
			if s.Max > 1 {
				row.Level = 2
			}
		}
		index[s.Segment] = len(rows)
		rows = append(rows, row)
	}
	return rows, nil
}

// applyExtensionLog reads the markers of the generated report.
func applyExtensionLog(res *ExtensionResult, lines []string) error {
	done := false
	var failure string
	for _, line := range lines {
		i := strings.Index(line, extensionMarker)
		if i < 0 {
			continue
		}
		key, value, _ := strings.Cut(strings.TrimSpace(line[i+len(extensionMarker):]), "=")
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
		case "RELEASED":
			res.Released = value
		case "CLOSED":
			res.Closed = value == "X"
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

// extensionReport is what the generated report is to do.
type extensionReport struct {
	Op          string // CREATE, CHANGE, DELETE
	Name        string
	BasicType   string
	Description string
	Package     string
	Transport   string
	Syntax      []extensionRow
	SetSyntax   bool
	Release     string // X, -, or blank: see releaseFlag
	Released    string // the release the extension was released in, if it was
}

// extensionReportSource is the report behind the three operations.
func extensionReportSource(prog string, r extensionReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REPORT %s.\n* Generated by vsp: %s IDoc extension %s. Deleted after the run.\n", strings.ToLower(prog), strings.ToLower(r.Op), r.Name)
	fmt.Fprintf(&b, "CONSTANTS: gc_op TYPE c LENGTH 6 VALUE %s,\n", abapLiteral(r.Op))
	fmt.Fprintf(&b, "           gc_cim TYPE edi_iapi00-cimtyp VALUE %s,\n", abapLiteral(r.Name))
	fmt.Fprintf(&b, "           gc_set_syntax TYPE c VALUE %s,\n", abapBool(r.SetSyntax))
	fmt.Fprintf(&b, "           gc_release TYPE c VALUE %s.\n", abapLiteral(r.Release))
	b.WriteString(`DATA: gt_syn TYPE STANDARD TABLE OF edi_iapi03,
      gs_syn TYPE edi_iapi03,
      gs_in TYPE edi_iapi05,
      gs_out TYPE edi_iapi01,
      gs_attr TYPE edi_iapi01,
      gv_order TYPE edi_iapi00-trkorr,
      gv_pkg TYPE edi_iapi00-devclass,
      gv_reopened TYPE c.

START-OF-SELECTION.
`)
	fmt.Fprintf(&b, "  gv_order = %s.\n  gv_pkg = %s.\n", abapLiteral(r.Transport), abapLiteral(r.Package))
	fmt.Fprintf(&b, "  gs_in-idoctyp = %s.\n  gs_in-descrp = %s.\n", abapLiteral(r.BasicType), abapLiteral(r.Description))
	for _, row := range r.Syntax {
		fmt.Fprintf(&b, "  CLEAR gs_syn.\n  gs_syn-refsegtyp = %s.\n  gs_syn-nr = %d.\n  gs_syn-segtyp = %s.\n", abapLiteral(row.Ref), row.Nr, abapLiteral(row.Segment))
		if row.Parent != "" {
			fmt.Fprintf(&b, "  gs_syn-parseg = %s.\n  gs_syn-parpno = %d.\n", abapLiteral(row.Parent), row.ParentNr)
		}
		fmt.Fprintf(&b, "  gs_syn-hlevel = %d.\n  gs_syn-occmin = %d.\n  gs_syn-occmax = %d.\n  gs_syn-mustfl = %s.\n  gs_syn-parflg = %s.\n  APPEND gs_syn TO gt_syn.\n",
			row.Level, row.Min, row.Max, abapBool(row.Mandatory), abapBool(row.HasChildren))
	}
	b.WriteString(`  CASE gc_op.
    WHEN 'CREATE'.
      CALL FUNCTION 'EXTTYPE_CREATE'
        EXPORTING pi_cimtyp = gc_cim
                  pi_devclass = gv_pkg
                  pi_attributes = gs_in
        IMPORTING pe_attributes = gs_out
        TABLES pt_syntax = gt_syn
        CHANGING pc_order = gv_order
        EXCEPTIONS OTHERS = 1.
      IF sy-subrc <> 0.
        PERFORM fail USING 'EXTTYPE_CREATE'.
      ENDIF.
      PERFORM step USING 'CREATE'.
      IF gc_release = 'X'.
        PERFORM close.
      ENDIF.
    WHEN 'CHANGE'.
      IF gc_set_syntax = 'X'.
* EXTTYPE_UPDATE takes a released extension as it is; only WE30 asks for
* the release to be cancelled first.
        CALL FUNCTION 'EXTTYPE_UPDATE'
          EXPORTING pi_cimtyp = gc_cim
                    pi_attributes = gs_in
          IMPORTING pe_attributes = gs_out
          TABLES pt_syntax = gt_syn
          CHANGING pc_order = gv_order
          EXCEPTIONS OTHERS = 1.
        IF sy-subrc <> 0.
          PERFORM fail USING 'EXTTYPE_UPDATE'.
        ENDIF.
        PERFORM step USING 'UPDATE'.
      ENDIF.
      PERFORM attributes.
      IF gs_attr-closed IS INITIAL AND gc_release = 'X'.
        PERFORM close.
      ELSEIF gs_attr-closed = 'X' AND gc_release = '-'.
        PERFORM reopen.
      ENDIF.
    WHEN 'DELETE'.
      PERFORM attributes.
      IF gs_attr-closed = 'X'.
        PERFORM reopen.
      ENDIF.
      CALL FUNCTION 'EXTTYPE_DELETE'
        EXPORTING pi_cimtyp = gc_cim
        CHANGING pc_order = gv_order
        EXCEPTIONS OTHERS = 1.
      IF sy-subrc <> 0.
        PERFORM fail USING 'EXTTYPE_DELETE'.
      ENDIF.
      CLEAR gv_reopened.
      PERFORM step USING 'DELETE'.
      PERFORM say USING 'DELETED' 'X'.
      PERFORM say USING 'OK' 'X'.
      RETURN.
  ENDCASE.
  PERFORM attributes.
  PERFORM say USING 'CLOSED' gs_attr-closed.
  PERFORM say USING 'RELEASED' gs_attr-released.
  PERFORM say USING 'OK' 'X'.

FORM attributes.
  CLEAR gs_attr.
  CALL FUNCTION 'EXTTYPE_EXISTENCE_CHECK'
    EXPORTING pi_cimtyp = gc_cim
    IMPORTING pe_attributes = gs_attr
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'EXTTYPE_EXISTENCE_CHECK'.
  ENDIF.
ENDFORM.

FORM close.
  CALL FUNCTION 'EXTTYPE_CLOSE'
    EXPORTING pi_cimtyp = gc_cim
    CHANGING pc_order = gv_order
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'EXTTYPE_CLOSE'.
  ENDIF.
  CLEAR gv_reopened.
  PERFORM step USING 'CLOSE'.
ENDFORM.

* What EXTTYPE_UNCLOSE does once its confirmation popup was answered with
* yes. The popup comes up for every customer extension (its existence check
* returns no package, so the component release it compares never matches),
* and in a background job it is answered with no. vsp only calls this for an
* extension released in the running release (checked by the caller and here).
FORM reopen.
  DATA: lv_view TYPE edview-ediview,
        lv_rc TYPE sy-subrc.
  CALL FUNCTION 'EXTTYPE_AUTHORITY_CHECK'
    EXPORTING pi_cimtyp = gc_cim
              pi_activity = '43'
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'EXTTYPE_AUTHORITY_CHECK'.
  ENDIF.
  PERFORM attributes.
  IF gs_attr-succtyp IS NOT INITIAL.
    PERFORM fail_text USING 'release' 'the extension has a successor; cancel the release of the last one'.
  ENDIF.
  IF gs_attr-released <> sy-saprl.
    PERFORM fail_text USING 'release' 'released in an earlier SAP release; not cancelled (note 844899)'.
  ENDIF.
  SELECT SINGLE ediview FROM edview INTO lv_view WHERE cimtyp = gc_cim.
  IF sy-subrc = 0.
    PERFORM fail_text USING 'release' 'a view uses the extension (EDVIEW); not cancelled'.
  ENDIF.
  CALL FUNCTION 'EXTTYPE_LOCK'
    EXPORTING pi_cimtyp = gc_cim
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'EXTTYPE_LOCK'.
  ENDIF.
  CALL FUNCTION 'EXTTYPE_TRANSPORT'
    EXPORTING pi_cimtyp = gc_cim
    CHANGING pc_order = gv_order
    EXCEPTIONS OTHERS = 1.
  lv_rc = sy-subrc.
  IF lv_rc = 0.
    UPDATE edcim SET closed = space released = space applrel = space
                     plast = sy-uname ldate = sy-datum ltime = sy-uzeit
               WHERE cimtyp = gc_cim.
  ENDIF.
  CALL FUNCTION 'EXTTYPE_UNLOCK'
    EXPORTING pi_cimtyp = gc_cim.
  IF lv_rc <> 0.
    sy-subrc = lv_rc.
    PERFORM fail USING 'EXTTYPE_TRANSPORT'.
  ENDIF.
  PERFORM step USING 'REOPEN'.
  gv_reopened = 'X'.
ENDFORM.

FORM step USING iv_step TYPE csequence.
  COMMIT WORK AND WAIT.
  PERFORM say USING 'STEP' iv_step.
ENDFORM.

FORM say USING iv_key TYPE csequence iv_value TYPE clike.
  DATA lv_text TYPE string.
  CONCATENATE 'VSP-IEXT:' iv_key '=' iv_value INTO lv_text.
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
* A release cancelled for this run is restored when a later step fails.
  IF gv_reopened = 'X'.
    CLEAR gv_reopened.
    CALL FUNCTION 'EXTTYPE_CLOSE'
      EXPORTING pi_cimtyp = gc_cim
      CHANGING pc_order = gv_order
      EXCEPTIONS OTHERS = 1.
    IF sy-subrc = 0.
      COMMIT WORK AND WAIT.
      PERFORM say USING 'WARN' 'The release was cancelled for this change and is in force again, as before.'.
    ELSE.
      ROLLBACK WORK.
      PERFORM say USING 'WARN' 'The release was cancelled for this change and could not be restored; the extension is not released now.'.
    ENDIF.
  ENDIF.
  LEAVE PROGRAM.
ENDFORM.
`)
	return b.String()
}
