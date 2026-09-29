package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	openrfc "github.com/oisee/open-rfc-go/rfc"
	"github.com/oisee/vibing-steampunk/pkg/adt"
	"github.com/oisee/vibing-steampunk/pkg/saprfc"
)

// classicBadiWait is how long a create or delete report may run. Both take a
// few seconds; the margin covers a busy batch queue.
const classicBadiWait = 3 * time.Minute

// rfcClassicBadiRunner runs the generated SXCI reports over RFC: XBP for the
// background job, RFC_READ_TABLE for TADIR.
type rfcClassicBadiRunner struct {
	c *openrfc.Client
}

func (r rfcClassicBadiRunner) RunReport(ctx context.Context, program string) ([]string, error) {
	run, err := saprfc.RunReportWith(ctx, r.c, saprfc.ReportRequest{Report: program, JobName: "VSP_" + program, Wait: classicBadiWait})
	if err != nil {
		return nil, err
	}
	if run.Status != "F" && run.Status != "A" {
		return nil, fmt.Errorf("job %s / %s has not ended after %s (status %s); the temporary report is deleted, so check SM37 before retrying",
			run.JobName, run.JobCount, classicBadiWait, run.StatusFor)
	}
	log, err := saprfc.ReadJobLog(ctx, r.c, run.JobName, run.JobCount)
	if err != nil {
		return nil, fmt.Errorf("job %s / %s ended (%s), but its log could not be read: %w", run.JobName, run.JobCount, run.StatusFor, err)
	}
	lines := make([]string, 0, len(log))
	for _, e := range log {
		lines = append(lines, e.Text)
	}
	return lines, nil
}

func (r rfcClassicBadiRunner) ObjectPackage(ctx context.Context, object, name string) (string, bool, error) {
	where := fmt.Sprintf("PGMID = 'R3TR' AND OBJECT = '%s' AND OBJ_NAME = '%s'", object, strings.ReplaceAll(name, "'", "''"))
	rows, err := saprfc.ReadTable(ctx, r.c, "TADIR", where, []string{"DEVCLASS"}, 1)
	if err != nil {
		return "", false, fmt.Errorf("reading TADIR: %w", err)
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return strings.TrimSpace(rows[0]["DEVCLASS"]), true, nil
}

// withClassicBadiRunner hands fn a runner on this server's RFC connection.
func (s *Server) withClassicBadiRunner(ctx context.Context, args map[string]any, fn func(adt.ClassicBadiRunner) (*adt.ClassicBadiResult, error)) (*mcp.CallToolResult, error) {
	c, release, err := s.rfcClientFor(ctx, args)
	if err != nil {
		return newToolResultError("classic BAdI implementations need the RFC connection: " + err.Error()), nil
	}
	defer release()
	res, err := fn(rfcClassicBadiRunner{c: c})
	if err != nil {
		if errors.Is(err, openrfc.ErrTransport) || errors.Is(err, openrfc.ErrClosed) {
			s.dropSharedRFC(ctx)
		}
		if res != nil && res.Program != "" {
			return newToolResultError(fmt.Sprintf("%v (temporary report %s)", err, res.Program)), nil
		}
		return newToolResultError(err.Error()), nil
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

// handleCreateClassicBadi creates a classic BAdI implementation (SE19, SXCI):
// SAP(action="create", target="SXCI", params={"name": "ZIMP", "badi": "BADI_X", ...}).
func (s *Server) handleCreateClassicBadi(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	o := adt.ClassicBadiImplementation{
		Name:        getStringParam(args, "name"),
		Badi:        getStringParam(args, "badi"),
		Class:       getStringParam(args, "class"),
		Description: getStringParam(args, "description"),
		Package:     getStringParam(args, "package"),
		Transport:   getStringParam(args, "transport"),
		Language:    strings.ToUpper(getStringParam(args, "language")),
		Activate:    true,
	}
	if v, ok := getBoolParam(args, "activate"); ok {
		o.Activate = v
	}
	switch f := args["filters"].(type) {
	case nil:
	case string:
		o.Filters = []string{f}
	case []any:
		for _, v := range f {
			o.Filters = append(o.Filters, fmt.Sprint(v))
		}
	default:
		return newToolResultError("filters must be a list of filter values"), nil
	}
	return s.withClassicBadiRunner(ctx, args, func(r adt.ClassicBadiRunner) (*adt.ClassicBadiResult, error) {
		return s.adtClient.CreateClassicBadiImplementation(ctx, o, r)
	})
}

// handleDeleteClassicBadi deletes a classic BAdI implementation:
// SAP(action="delete", target="SXCI ZIMP", params={"transport": "..."}).
func (s *Server) handleDeleteClassicBadi(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	keep, _ := getBoolParam(args, "keep_class")
	return s.withClassicBadiRunner(ctx, args, func(r adt.ClassicBadiRunner) (*adt.ClassicBadiResult, error) {
		return s.adtClient.DeleteClassicBadiImplementation(ctx, getStringParam(args, "name"), getStringParam(args, "transport"), keep, r)
	})
}
