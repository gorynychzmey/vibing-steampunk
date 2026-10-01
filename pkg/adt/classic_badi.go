package adt

import (
	"context"
	"fmt"
	"strings"
)

// Classic BAdI implementations (SXCI) are the one kind of enhancement that
// neither ADT nor any RFC-enabled module can create: SE19 builds them in
// SXO_IMPL_SAVE, which takes a CL_BADI_FLT_VALUES_ALV -- a GUI ALV grid that
// ends a background job and kills an HTTP session. The steps behind it are
// ordinary function modules and classes, though, and replayed in the same
// order without the grid they create an implementation SE19 reads back
// unchanged. None of them is remote-enabled, so vsp writes the sequence into a
// temporary report, runs that as a background job and reads the outcome from
// the job log.
//
// A classic BAdI that SAP migrated to an enhancement spot (SXS_ATTR has a
// MIG_ENHSPOTNAME) is better implemented as a new-style BAdI implementation
// (ENHO); SXCI stays the only way for the ones that were never migrated.

// ClassicBadiImplementation describes one classic BAdI implementation.
type ClassicBadiImplementation struct {
	Name        string   // implementation name (SXC_ATTR-IMP_NAME)
	Badi        string   // BAdI definition (SXS_ATTR-EXIT_NAME)
	Class       string   // implementing class; proposed from the name when empty
	Description string   // short text (SXC_ATTRT-TEXT, 60 characters)
	Package     string   // defaults to $TMP
	Transport   string   // required for a transportable package
	Language    string   // SAP (D) or ISO (DE) language key; defaults to the job's logon language
	Filters     []string // filter values, for a filter-dependent BAdI
	Activate    bool
}

// ClassicBadiResult is what the generated report reported back.
type ClassicBadiResult struct {
	Name      string   `json:"name"`
	Badi      string   `json:"badi,omitempty"`
	Class     string   `json:"class,omitempty"`
	Package   string   `json:"package,omitempty"`
	Transport string   `json:"transport,omitempty"`
	Active    bool     `json:"active"`
	Deleted   bool     `json:"deleted,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Program   string   `json:"program"`
}

const classicBadiMarker = "VSP-SXCI:"

// CreateClassicBadiImplementation creates a classic BAdI implementation and,
// with Activate, activates it.
func (c *Client) CreateClassicBadiImplementation(ctx context.Context, o ClassicBadiImplementation, run ClassicBadiRunner) (*ClassicBadiResult, error) {
	o.Name = strings.ToUpper(strings.TrimSpace(o.Name))
	o.Badi = strings.ToUpper(strings.TrimSpace(o.Badi))
	o.Class = strings.ToUpper(strings.TrimSpace(o.Class))
	o.Package = strings.ToUpper(strings.TrimSpace(o.Package))
	o.Transport = strings.ToUpper(strings.TrimSpace(o.Transport))
	if o.Package == "" {
		o.Package = "$TMP"
	}
	o.Language = strings.ToUpper(strings.TrimSpace(o.Language))
	if err := validateClassicBadi(o); err != nil {
		return nil, err
	}
	if err := c.checkMutation(ctx, MutationContext{
		Op: OpCreate, OpName: "CreateClassicBadiImplementation",
		Package: o.Package, Transport: o.Transport,
	}); err != nil {
		return nil, err
	}
	if _, exists, err := objectPackage(ctx, run, "SXCI", o.Name); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("classic BAdI implementation %s already exists", o.Name)
	}

	res := &ClassicBadiResult{Name: o.Name, Badi: o.Badi, Package: o.Package, Transport: o.Transport}
	tr, err := c.runTempReport(ctx, "ZTEMP_SXCI_", func(prog string) string {
		return classicBadiCreateSource(prog, o)
	}, run)
	res.Program = tr.Program
	res.Warnings = append(res.Warnings, tr.Warnings...)
	if err != nil {
		return res, err
	}
	return res, applyClassicBadiLog(res, tr.Lines)
}

// DeleteClassicBadiImplementation deletes a classic BAdI implementation and,
// unless keepClass, its implementing class when no other implementation
// uses it.
func (c *Client) DeleteClassicBadiImplementation(ctx context.Context, name, transport string, keepClass bool, run ClassicBadiRunner) (*ClassicBadiResult, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	transport = strings.ToUpper(strings.TrimSpace(transport))
	if err := validateABAPName("implementation name", name, 20); err != nil {
		return nil, err
	}
	pkg, exists, err := objectPackage(ctx, run, "SXCI", name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("classic BAdI implementation %s does not exist", name)
	}
	if !strings.HasPrefix(pkg, "$") && transport == "" {
		return nil, fmt.Errorf("%s is in the transportable package %s: a transport request is required", name, pkg)
	}
	if err = c.checkMutation(ctx, MutationContext{
		Op: OpDelete, OpName: "DeleteClassicBadiImplementation",
		Package: pkg, Transport: transport,
	}); err != nil {
		return nil, err
	}

	res := &ClassicBadiResult{Name: name, Package: pkg, Transport: transport}
	tr, err := c.runTempReport(ctx, "ZTEMP_SXCD_", func(prog string) string {
		return classicBadiDeleteSource(prog, name, pkg, transport, keepClass)
	}, run)
	res.Program = tr.Program
	res.Warnings = append(res.Warnings, tr.Warnings...)
	if err != nil {
		return res, err
	}
	return res, applyClassicBadiLog(res, tr.Lines)
}

// applyClassicBadiLog reads the markers the generated report wrote into its
// job log. A report that ended without VSP-SXCI:OK failed, whether it said why
// or was cancelled before it could.
func applyClassicBadiLog(res *ClassicBadiResult, lines []string) error {
	done := false
	var failure string
	for _, line := range lines {
		i := strings.Index(line, classicBadiMarker)
		if i < 0 {
			continue
		}
		key, value, _ := strings.Cut(strings.TrimSpace(line[i+len(classicBadiMarker):]), "=")
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "OK":
			done = true
		case "ERR":
			failure = value
		case "WARN":
			res.Warnings = append(res.Warnings, value)
		case "CLASS":
			res.Class = value
		case "BADI":
			res.Badi = value
		case "KORR":
			res.Transport = value
		case "ACTIVE":
			res.Active = value == "X"
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

func validateClassicBadi(o ClassicBadiImplementation) error {
	if err := validateABAPName("implementation name", o.Name, 20); err != nil {
		return err
	}
	if err := validateABAPName("BAdI", o.Badi, 20); err != nil {
		return err
	}
	if o.Class != "" {
		if err := validateABAPName("class", o.Class, 30); err != nil {
			return err
		}
	}
	if err := validateABAPName("package", o.Package, 30); err != nil {
		return err
	}
	if o.Transport != "" {
		if err := validateABAPName("transport", o.Transport, 20); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(o.Package, "$") && o.Transport == "" {
		return fmt.Errorf("package %s is transportable: a transport request is required", o.Package)
	}
	if len([]rune(o.Description)) > 60 {
		return fmt.Errorf("the description has %d characters; SXC_ATTRT holds 60", len([]rune(o.Description)))
	}
	if n := len([]rune(o.Language)); n > 2 {
		return fmt.Errorf("language must be an SAP (D) or ISO (DE) language key, got %q", o.Language)
	}
	for _, s := range append([]string{o.Description, o.Language}, o.Filters...) {
		if strings.ContainsAny(s, "\r\n") {
			return fmt.Errorf("line breaks are not allowed in %q", s)
		}
	}
	for _, f := range o.Filters {
		if len([]rune(f)) > 120 {
			return fmt.Errorf("filter value %q is longer than 120 characters", f)
		}
	}
	return nil
}

// validateABAPName accepts what a repository name can hold: letters, digits,
// underscore, namespace slashes and the $ of local packages. Anything else
// could break out of the literal it is written into.
func validateABAPName(what, name string, max int) error {
	if name == "" {
		return fmt.Errorf("%s is required", what)
	}
	if len(name) > max {
		return fmt.Errorf("%s %s is longer than %d characters", what, name, max)
	}
	for _, r := range name {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '/' && r != '$' {
			return fmt.Errorf("%s %q contains %q", what, name, r)
		}
	}
	return nil
}

// abapLiteral quotes s as an ABAP character literal.
func abapLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func abapBool(b bool) string {
	if b {
		return "'X'"
	}
	return "' '"
}

// classicBadiCommon is shared by both reports: the markers and the failure
// exit, which turns the last SAP message into VSP-SXCI:ERR and ends the
// report. Nothing it writes is committed unless the report reaches its own
// COMMIT, so a report that stops half-way leaves the database as it was --
// except for what RS_CORR_INSERT already put into the request.
const classicBadiCommon = `
FORM say USING iv_key TYPE csequence iv_value TYPE csequence.
  DATA lv_text TYPE string.
  CONCATENATE 'VSP-SXCI:' iv_key '=' iv_value INTO lv_text.
  MESSAGE lv_text TYPE 'S'.
ENDFORM.

* In a background job SXI_CLASS_ENQUEUE treats the implementing class as a
* generated object (TADIR-GENFLAG = X), and generated objects are never
* recorded in a request. SE19 in dialog records the class; so does this.
FORM record_class USING iv_class TYPE seoclsname
                        iv_pkg TYPE devclass
                        iv_langu TYPE sy-langu
                  CHANGING cv_korr TYPE trkorr.
  DATA: lv_gen TYPE tadir-genflag,
        lv_obj TYPE tadir-obj_name,
        lv_pkg TYPE devclass.
  lv_obj = iv_class.
  SELECT SINGLE genflag FROM tadir INTO lv_gen
    WHERE pgmid = 'R3TR' AND object = 'CLAS' AND obj_name = lv_obj.
  IF sy-subrc <> 0.
    RETURN.
  ENDIF.
  IF lv_gen IS NOT INITIAL.
    CALL FUNCTION 'TR_TADIR_INTERFACE'
      EXPORTING wi_remove_genflag = 'X'
                wi_test_modus = ' '
                wi_tadir_pgmid = 'R3TR'
                wi_tadir_object = 'CLAS'
                wi_tadir_obj_name = lv_obj
      EXCEPTIONS OTHERS = 1.
    IF sy-subrc <> 0.
      PERFORM record_fail USING 'TR_TADIR_INTERFACE (generation flag of the class)'.
      RETURN.
    ENDIF.
  ENDIF.
  CHECK iv_pkg(1) <> '$'.
  lv_pkg = iv_pkg.
  CALL FUNCTION 'RS_CORR_INSERT'
    EXPORTING object = iv_class
              object_class = 'CLAS'
              mode = seex_access_modify
              global_lock = seex_true
              master_language = iv_langu
              devclass = lv_pkg
              korrnum = cv_korr
              suppress_dialog = seex_true
    IMPORTING korrnum = cv_korr
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM record_fail USING 'RS_CORR_INSERT (class)'.
    RETURN.
  ENDIF.
ENDFORM.

* After the implementation is committed a failure here cannot be rolled
* back; reporting ERR would call a saved implementation failed, and a retry
* would only find that it already exists. It is a warning then.
FORM record_fail USING iv_step TYPE csequence.
  DATA lv_msg TYPE string.
  IF gv_record_soft IS INITIAL.
    PERFORM fail USING iv_step.
  ENDIF.
  IF sy-msgid IS NOT INITIAL.
    MESSAGE ID sy-msgid TYPE 'S' NUMBER sy-msgno
            WITH sy-msgv1 sy-msgv2 sy-msgv3 sy-msgv4 INTO lv_msg.
  ENDIF.
  lv_msg = |The implementation is saved, but { iv_step } failed: { lv_msg }. Record the class in the request yourself.|.
  PERFORM say USING 'WARN' lv_msg.
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
  LEAVE PROGRAM.
ENDFORM.
`

// classicBadiCreateSource is the report that creates one implementation. The
// order follows SXO_IMPL_SAVE; what the GUI grid contributed there -- the
// filter values -- is built directly in CL_BADI_FLT_DATA_TRANS_AND_DB.
func classicBadiCreateSource(prog string, o ClassicBadiImplementation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REPORT %s.\n* Generated by vsp: create classic BAdI implementation %s. Deleted after the run.\n", strings.ToLower(prog), o.Name)
	b.WriteString("DATA gv_record_soft TYPE c. \" set once the implementation is committed\n")
	b.WriteString("TYPE-POOLS: seex, smodi.\n")
	fmt.Fprintf(&b, "CONSTANTS: gc_imp TYPE exit_imp VALUE %s,\n", abapLiteral(o.Name))
	fmt.Fprintf(&b, "           gc_exit TYPE exit_def VALUE %s,\n", abapLiteral(o.Badi))
	fmt.Fprintf(&b, "           gc_activate TYPE seex_boolean VALUE %s.\n", abapBool(o.Activate))
	b.WriteString("DATA: gv_class TYPE seoclsname,\n      gv_pkg TYPE devclass,\n      gv_korr TYPE trkorr,\n      gv_iso TYPE laiso,\n      gc_langu TYPE sy-langu,\n      gv_text TYPE sxc_attrt-text.\n")
	b.WriteString(`DATA: gs_badi LIKE badi_data,
      gv_mast TYPE sy-langu,
      gv_ext TYPE seoclsname,
      go_flt TYPE REF TO cl_badi_flt_struct,
      go_ref TYPE REF TO cl_badi_flt_data_trans_and_db,
      gt_flt TYPE sxrt_filter_table,
      gs_flt LIKE LINE OF gt_flt,
      gt_prot TYPE sprot_u_tab,
      gs_prot TYPE sprot_u,
      gt_fcodes TYPE seex_fcode_table,
      gt_cocos TYPE seex_coco_table,
      gt_intas TYPE seex_table_table,
      gt_scrns TYPE seex_screen_table,
      gt_methods TYPE seex_mtd_table,
      gt_no_fcodes TYPE seex_fcode_table,
      gt_no_cocos TYPE seex_coco_table,
      gt_no_intas TYPE seex_table_table,
      gt_no_scrns TYPE seex_screen_table,
      gv_mig TYPE enhspotname,
      go_err TYPE REF TO cx_root,
      gv_text_err TYPE string.

START-OF-SELECTION.
`)
	fmt.Fprintf(&b, "  gv_class = %s.\n  gv_pkg = %s.\n  gv_korr = %s.\n  gv_text = %s.\n  gv_iso = %s.\n",
		abapLiteral(o.Class), abapLiteral(o.Package), abapLiteral(o.Transport), abapLiteral(o.Description), abapLiteral(o.Language))
	for _, f := range o.Filters {
		fmt.Fprintf(&b, "  CLEAR gs_flt.\n  gs_flt-flt_val = %s.\n  APPEND gs_flt TO gt_flt.\n", abapLiteral(f))
	}
	b.WriteString(`
  IF gv_iso IS INITIAL.
    gc_langu = sy-langu.
  ELSEIF strlen( gv_iso ) = 1.
    gc_langu = gv_iso.
  ELSE.
    CALL FUNCTION 'CONVERSION_EXIT_ISOLA_INPUT'
      EXPORTING input = gv_iso
      IMPORTING output = gc_langu
      EXCEPTIONS OTHERS = 1.
    IF sy-subrc <> 0.
      PERFORM fail USING 'language'.
    ENDIF.
  ENDIF.

  CALL FUNCTION 'SXV_IMP_EXISTS'
    EXPORTING imp_name = gc_imp
    EXCEPTIONS not_existing = 1 OTHERS = 2.
  IF sy-subrc <> 1.
    CONCATENATE 'Implementation' gc_imp 'already exists' INTO gv_text_err SEPARATED BY space.
    PERFORM fail_text USING 'SXV_IMP_EXISTS' gv_text_err.
  ENDIF.

  CALL FUNCTION 'SXO_BADI_READ'
    EXPORTING exit_name = gc_exit
              maint_langu = gc_langu
    IMPORTING badi = gs_badi
              mast_langu = gv_mast
              ext_clname = gv_ext
              filter_obj = go_flt
    TABLES fcodes = gt_fcodes
           cocos = gt_cocos
           intas = gt_intas
           scrns = gt_scrns
           methods = gt_methods
    EXCEPTIONS read_failure = 1 OTHERS = 2.
  IF sy-subrc <> 0.
    CONCATENATE 'BAdI definition' gc_exit 'does not exist' INTO gv_text_err SEPARATED BY space.
    PERFORM fail_text USING 'SXO_BADI_READ' gv_text_err.
  ENDIF.
  PERFORM say USING 'BADI' gs_badi-exit_name.
  IF gt_fcodes IS NOT INITIAL OR gt_scrns IS NOT INITIAL.
    PERFORM say USING 'WARN' 'The BAdI has menu or screen enhancements; they are not implemented, do that in SE19'.
  ENDIF.
  SELECT SINGLE mig_enhspotname FROM sxs_attr INTO gv_mig WHERE exit_name = gc_exit.
  IF gv_mig IS NOT INITIAL.
    CONCATENATE 'The BAdI was migrated to enhancement spot' gv_mig '- an ENHO BAdI implementation is the modern alternative' INTO gv_text_err SEPARATED BY space.
    PERFORM say USING 'WARN' gv_text_err.
  ENDIF.

  IF gs_badi-flt_type IS INITIAL AND gt_flt IS NOT INITIAL.
    CONCATENATE 'BAdI' gc_exit 'is not filter-dependent' INTO gv_text_err SEPARATED BY space.
    PERFORM fail_text USING 'filters' gv_text_err.
  ENDIF.
  IF gs_badi-flt_type IS NOT INITIAL AND gt_flt IS INITIAL.
    CONCATENATE 'BAdI' gc_exit 'is filter-dependent, filter type' gs_badi-flt_type INTO gv_text_err SEPARATED BY space.
    PERFORM fail_text USING 'filters' gv_text_err.
  ENDIF.
  IF gs_badi-flt_type IS NOT INITIAL AND go_flt IS NOT BOUND.
    CONCATENATE 'Filter type' gs_badi-flt_type 'cannot be used' INTO gv_text_err SEPARATED BY space.
    PERFORM fail_text USING 'filters' gv_text_err.
  ENDIF.
  IF go_flt IS BOUND.
    CREATE OBJECT go_ref EXPORTING filter_values = gt_flt filter_obj = go_flt.
    IF gt_flt IS NOT INITIAL.
      go_ref->flt_val_check( IMPORTING prot = gt_prot ).
      LOOP AT gt_prot INTO gs_prot WHERE severity = 'E' OR severity = 'A'.
        sy-msgid = gs_prot-ag. sy-msgno = gs_prot-msgnr.
        sy-msgv1 = gs_prot-var1. sy-msgv2 = gs_prot-var2.
        sy-msgv3 = gs_prot-var3. sy-msgv4 = gs_prot-var4.
        PERFORM fail USING 'filter value check'.
      ENDLOOP.
    ENDIF.
  ENDIF.

  IF gv_class IS INITIAL.
    CALL FUNCTION 'SXV_IMP_CLASS_NAME_PROVIDE'
      EXPORTING imp_name = gc_imp
                exit_name = gc_exit
      CHANGING imp_class = gv_class
      EXCEPTIONS OTHERS = 1.
    IF sy-subrc <> 0 OR gv_class IS INITIAL.
      PERFORM fail USING 'SXV_IMP_CLASS_NAME_PROVIDE'.
    ENDIF.
  ENDIF.

  CALL FUNCTION 'RS_CORR_INSERT'
    EXPORTING object = gc_imp
              object_class = seex_imp_ob_class
              mode = seex_access_insert
              global_lock = seex_true
              master_language = gc_langu
              extend = seex_true
              devclass = gv_pkg
              korrnum = gv_korr
              suppress_dialog = seex_true
    IMPORTING devclass = gv_pkg
              korrnum = gv_korr
    EXCEPTIONS cancelled = 1 permission_failure = 2 unknown_objectclass = 3 OTHERS = 4.
  IF sy-subrc <> 0.
    PERFORM fail USING 'RS_CORR_INSERT'.
  ENDIF.

  CALL FUNCTION 'SXV_IMP_CLASS_CREATE'
    EXPORTING imp_name = gc_imp
              exit_name = gc_exit
              inter_name = gs_badi-inter_name
              language = gc_langu
              genflag = seex_false
    CHANGING imp_class = gv_class
             korrnum = gv_korr
             devclass = gv_pkg
    EXCEPTIONS failure = 1 OTHERS = 2.
  IF sy-subrc <> 0.
    PERFORM fail USING 'SXV_IMP_CLASS_CREATE'.
  ENDIF.
  PERFORM say USING 'CLASS' gv_class.

  IF go_ref IS BOUND.
    go_ref->save( EXPORTING maint_langu = gc_langu
                            imp_name = gc_imp
                            no_dialog = seex_true
                            devclass = gv_pkg
                  CHANGING korrnum = gv_korr
                  EXCEPTIONS OTHERS = 1 ).
    IF sy-subrc <> 0.
      PERFORM fail USING 'filter values'.
    ENDIF.
  ENDIF.

  cl_badi_components=>save( EXPORTING role = seex_role_imp
                                      fcodes = gt_no_fcodes
                                      cocos = gt_no_cocos
                                      intas = gt_no_intas
                                      scrns = gt_no_scrns
                            EXCEPTIONS OTHERS = 1 ).
  IF sy-subrc <> 0.
    PERFORM fail USING 'CL_BADI_COMPONENTS=>SAVE'.
  ENDIF.

  DATA: go_log TYPE REF TO if_clm_tool_log,
        gs_trkey TYPE trkey,
        gt_smodi TYPE smodi_tool_log_tab,
        gs_smodi TYPE smodi_tool_log_struct.
  gs_trkey-obj_type = seex_imp_ob_class.
  gs_trkey-obj_name = gc_imp.
  gs_trkey-sub_type = seex_imp_ob_class.
  gs_trkey-sub_name = gc_imp.
  CALL FUNCTION 'CLM_CREATE_TOOL_LOG_OBJECT'
    EXPORTING p_trkey = gs_trkey
              p_state = smodi_c_state_active
    CHANGING p_log_object = go_log.
  go_log->get_entries( IMPORTING p_entries_tab = gt_smodi ).
  IF gt_smodi IS INITIAL.
    gs_smodi-operation = smodi_c_op_badi_imp.
    APPEND gs_smodi TO gt_smodi.
  ELSE.
    LOOP AT gt_smodi INTO gs_smodi.
      gs_smodi-operation = smodi_c_op_badi_imp.
      MODIFY gt_smodi FROM gs_smodi.
    ENDLOOP.
  ENDIF.
  go_log->modify_entries( p_entries_tab = gt_smodi ).
  go_log->main_prog = gc_exit.
  go_log->mod_langu = gc_langu.
  go_log->save( p_state = smodi_c_state_active p_trkorr = gv_korr ).

  DATA: gs_class TYPE sxc_class,
        gs_attr TYPE sxc_attr,
        gs_attrt TYPE sxc_attrt.
  DELETE FROM sxc_class WHERE imp_name = gc_imp.
  gs_class-imp_name = gc_imp.
  gs_class-inter_name = gs_badi-inter_name.
  gs_class-imp_class = gv_class.
  INSERT sxc_class FROM gs_class.

  IF go_ref IS BOUND.
    go_ref->save_sxc_exit( exit_name = gc_exit imp_name = gc_imp ).
  ELSE.
    DATA gs_exit TYPE sxc_exit.
    DELETE FROM sxc_exit WHERE imp_name = gc_imp.
    gs_exit-exit_name = gc_exit.
    gs_exit-imp_name = gc_imp.
    INSERT sxc_exit FROM gs_exit.
  ENDIF.

  gs_attr-imp_name = gc_imp.
  gs_attr-uname = sy-uname.
  gs_attr-udate = sy-datum.
  gs_attr-utime = sy-uzeit.
  gs_attr-mst_lang = gc_langu.
  MODIFY sxc_attr FROM gs_attr.

  gs_attrt-sprsl = gc_langu.
  gs_attrt-imp_name = gc_imp.
  gs_attrt-text = gv_text.
  MODIFY sxc_attrt FROM gs_attrt.

  IF gv_mig IS NOT INITIAL.
    TRY.
        cl_enh_classic_badi_migration=>update_badi_implementation( imp_name = gc_imp ).
      CATCH cx_root INTO go_err.
        gv_text_err = go_err->get_text( ).
        PERFORM say USING 'WARN' gv_text_err.
    ENDTRY.
  ENDIF.

  CALL FUNCTION 'DB_COMMIT'.
  COMMIT WORK AND WAIT.
  PERFORM say USING 'KORR' gv_korr.

  IF gc_activate = seex_true.
* With no_dialog SXO_IMPL_ACTIVE raises nothing: whatever stops the
* activation is only written to the protocol.
    CLEAR gt_prot.
    CALL FUNCTION 'SXO_IMPL_ACTIVE'
      EXPORTING imp_name = gc_imp
                no_dialog = seex_true
      CHANGING protocol = gt_prot
      EXCEPTIONS OTHERS = 1.
    IF sy-subrc <> 0.
      MESSAGE ID sy-msgid TYPE 'S' NUMBER sy-msgno
              WITH sy-msgv1 sy-msgv2 sy-msgv3 sy-msgv4 INTO gv_text_err.
      CONCATENATE 'Created, but not activated:' gv_text_err INTO gv_text_err SEPARATED BY space.
      PERFORM say USING 'WARN' gv_text_err.
    ELSE.
      COMMIT WORK AND WAIT.
    ENDIF.
    LOOP AT gt_prot INTO gs_prot WHERE severity = 'E' OR severity = 'A' OR severity = 'W'.
      MESSAGE ID gs_prot-ag TYPE 'S' NUMBER gs_prot-msgnr
              WITH gs_prot-var1 gs_prot-var2 gs_prot-var3 gs_prot-var4 INTO gv_text_err.
      gv_text_err = |Activation { gs_prot-severity }: { gv_text_err } ({ gs_prot-ag } { gs_prot-msgnr })|.
      PERFORM say USING 'WARN' gv_text_err.
    ENDLOOP.
  ENDIF.
* Recorded only now: with the class already in the request, the activation
* above refuses it (ENHANCEMENT 575, "does not implement the interface").
  gv_record_soft = 'X'.
  PERFORM record_class USING gv_class gv_pkg gc_langu CHANGING gv_korr.
  COMMIT WORK AND WAIT.
  SELECT SINGLE active FROM sxc_attr INTO gs_attr-active WHERE imp_name = gc_imp.
  PERFORM say USING 'ACTIVE' gs_attr-active.
  PERFORM say USING 'OK' ''.
`)
	b.WriteString(classicBadiCommon)
	return b.String()
}

// classicBadiDeleteSource is the report that deletes one implementation: its
// class (SXV_IMP_CLASS_DELETE deletes it only when no other implementation
// uses it), its filter values, its rows in the SXC tables, its modification
// log and its TADIR entry.
func classicBadiDeleteSource(prog, name, pkg, transport string, keepClass bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REPORT %s.\n* Generated by vsp: delete classic BAdI implementation %s. Deleted after the run.\n", strings.ToLower(prog), name)
	b.WriteString("DATA gv_record_soft TYPE c. \" set once the implementation is committed\n")
	b.WriteString("TYPE-POOLS: seex, smodi.\n")
	fmt.Fprintf(&b, "CONSTANTS: gc_imp TYPE exit_imp VALUE %s,\n", abapLiteral(name))
	fmt.Fprintf(&b, "           gc_keep_class TYPE seex_boolean VALUE %s.\n", abapBool(keepClass))
	b.WriteString("DATA: gv_pkg TYPE devclass,\n      gv_korr TYPE trkorr.\n")
	b.WriteString(`DATA: gv_exit TYPE exit_def,
      gs_badi LIKE badi_data,
      go_flt TYPE REF TO cl_badi_flt_struct,
      go_ref TYPE REF TO cl_badi_flt_data_trans_and_db,
      gt_flt TYPE sxrt_filter_table,
      gs_class TYPE sxc_class,
      gv_mast TYPE sy-langu,
      gv_obj TYPE tadir-obj_name,
      gv_class TYPE seoclsname,
      gv_text_err TYPE string.

START-OF-SELECTION.
`)
	fmt.Fprintf(&b, "  gv_pkg = %s.\n  gv_korr = %s.\n", abapLiteral(pkg), abapLiteral(transport))
	b.WriteString(`
  CALL FUNCTION 'SXV_IMP_EXISTS'
    EXPORTING imp_name = gc_imp
    EXCEPTIONS not_existing = 1 OTHERS = 2.
  IF sy-subrc = 1.
    CONCATENATE 'Implementation' gc_imp 'does not exist' INTO gv_text_err SEPARATED BY space.
    PERFORM fail_text USING 'SXV_IMP_EXISTS' gv_text_err.
  ENDIF.

  CALL FUNCTION 'SXV_EXIT_FOR_IMP'
    EXPORTING imp_name = gc_imp
    IMPORTING exit_name = gv_exit
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'SXV_EXIT_FOR_IMP'.
  ENDIF.
  PERFORM say USING 'BADI' gv_exit.
  CALL FUNCTION 'SXO_BADI_READ'
    EXPORTING exit_name = gv_exit
    IMPORTING badi = gs_badi
              filter_obj = go_flt
    EXCEPTIONS OTHERS = 1.
  IF sy-subrc <> 0.
    PERFORM fail USING 'SXO_BADI_READ'.
  ENDIF.

  SELECT SINGLE masterlang FROM tadir INTO gv_mast
    WHERE pgmid = 'R3TR' AND object = seex_imp_ob_class AND obj_name = gc_imp.
  IF gv_pkg(1) <> '$'.
    CALL FUNCTION 'RS_CORR_INSERT'
      EXPORTING object = gc_imp
                object_class = seex_imp_ob_class
                mode = seex_access_modify
                global_lock = seex_true
                master_language = gv_mast
                devclass = gv_pkg
                korrnum = gv_korr
                suppress_dialog = seex_true
      IMPORTING devclass = gv_pkg
                korrnum = gv_korr
      EXCEPTIONS OTHERS = 1.
    IF sy-subrc <> 0.
      PERFORM fail USING 'RS_CORR_INSERT'.
    ENDIF.
  ENDIF.

  SELECT SINGLE * FROM sxc_class INTO gs_class WHERE imp_name = gc_imp.
  IF sy-subrc = 0 AND gc_keep_class = seex_false.
    gv_class = gs_class-imp_class.
    PERFORM record_class USING gv_class gv_pkg gv_mast CHANGING gv_korr.
    CALL FUNCTION 'SXV_IMP_CLASS_DELETE'
      EXPORTING imp_name = gc_imp
                inter_name = gs_class-inter_name
                no_dialog = seex_true
                cls_type = seex_cls_type_normal
                exit_name = gv_exit
                class_name = gs_class-imp_class
                preserve = seex_true
      CHANGING korrnum = gv_korr
               devclass = gv_pkg
      EXCEPTIONS OTHERS = 1.
    IF sy-subrc <> 0.
      PERFORM fail USING 'SXV_IMP_CLASS_DELETE'.
    ENDIF.
    PERFORM say USING 'CLASS' gs_class-imp_class.
  ENDIF.

  IF go_flt IS BOUND AND gs_badi-flt_ext = seex_true.
    CREATE OBJECT go_ref EXPORTING filter_values = gt_flt filter_obj = go_flt.
    go_ref->save( EXPORTING maint_langu = sy-langu
                            imp_name = gc_imp
                            no_dialog = seex_true
                            devclass = gv_pkg
                  CHANGING korrnum = gv_korr
                  EXCEPTIONS OTHERS = 1 ).
    IF sy-subrc <> 0.
      PERFORM fail USING 'filter values'.
    ENDIF.
  ENDIF.

  DELETE FROM sxc_attr WHERE imp_name = gc_imp.
  DELETE FROM sxc_attrt WHERE imp_name = gc_imp.
  DELETE FROM sxc_fcode WHERE imp_name = gc_imp.
  DELETE FROM sxc_fcodet WHERE imp_name = gc_imp.
  DELETE FROM sxc_coco WHERE imp_name = gc_imp.
  DELETE FROM sxc_scrn WHERE imp_name = gc_imp.
  DELETE FROM sxc_class WHERE imp_name = gc_imp.
  DELETE FROM sxc_exit WHERE imp_name = gc_imp.
  DELETE FROM sxc_impswh WHERE imp_name = gc_imp.

  DATA: go_log TYPE REF TO if_clm_tool_log,
        gs_trkey TYPE trkey.
  gs_trkey-obj_type = seex_imp_ob_class.
  gs_trkey-obj_name = gc_imp.
  gs_trkey-sub_type = seex_imp_ob_class.
  gs_trkey-sub_name = gc_imp.
  CALL FUNCTION 'CLM_CREATE_TOOL_LOG_OBJECT'
    EXPORTING p_trkey = gs_trkey
              p_state = smodi_c_state_active
    CHANGING p_log_object = go_log.
  IF go_log IS BOUND.
    go_log->delete( ).
  ENDIF.

* A local object loses its TADIR entry. A transportable one is locked in the
* request and keeps it with the deletion flag until the request is released,
* as the Class Builder leaves a deleted class.
  gv_obj = gc_imp.
  IF gv_pkg(1) = '$'.
    CALL FUNCTION 'TR_TADIR_INTERFACE'
      EXPORTING wi_delete_tadir_entry = 'X'
                wi_test_modus = ' '
                wi_tadir_pgmid = 'R3TR'
                wi_tadir_object = seex_imp_ob_class
                wi_tadir_obj_name = gv_obj
      EXCEPTIONS OTHERS = 1.
  ELSE.
    CALL FUNCTION 'TR_TADIR_INTERFACE'
      EXPORTING iv_delflag = 'X'
                wi_test_modus = ' '
                wi_tadir_pgmid = 'R3TR'
                wi_tadir_object = seex_imp_ob_class
                wi_tadir_obj_name = gv_obj
      EXCEPTIONS OTHERS = 1.
  ENDIF.
  IF sy-subrc <> 0.
    PERFORM fail USING 'TR_TADIR_INTERFACE'.
  ENDIF.

  CALL FUNCTION 'DB_COMMIT'.
  COMMIT WORK AND WAIT.
  PERFORM say USING 'KORR' gv_korr.
  PERFORM say USING 'DELETED' 'X'.
  PERFORM say USING 'OK' ''.
`)
	b.WriteString(classicBadiCommon)
	return b.String()
}
