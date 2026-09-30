package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	openrfc "github.com/oisee/open-rfc-go/rfc"
	"github.com/oisee/vibing-steampunk/pkg/adt"
)

// routeIDocSegmentAction routes IDoc segment types (WE31):
// read/create/edit/delete target="SEGM <name>".
func (s *Server) routeIDocSegmentAction(ctx context.Context, action, objectType, objectName string, params map[string]any) (*mcp.CallToolResult, bool, error) {
	if objectType != "SEGM" {
		return nil, false, nil
	}
	params = withName(params, objectName)
	switch action {
	case "read":
		return s.callHandler(ctx, s.handleReadIDocSegment, params)
	case "create":
		return s.callHandler(ctx, s.handleCreateIDocSegment, params)
	case "edit":
		return s.callHandler(ctx, s.handleChangeIDocSegment, params)
	case "delete":
		return s.callHandler(ctx, s.handleDeleteIDocSegment, params)
	}
	return nil, false, nil
}

// withBackgroundRunner hands fn a runner on this server's RFC connection and
// renders what it returns.
func (s *Server) withBackgroundRunner(ctx context.Context, args map[string]any, what string, fn func(adt.BackgroundRunner) (any, string, []string, error)) (*mcp.CallToolResult, error) {
	c, release, err := s.rfcClientFor(ctx, args)
	if err != nil {
		return newToolResultError(what + " need the RFC connection: " + err.Error()), nil
	}
	defer release()
	res, program, warnings, err := fn(rfcClassicBadiRunner{c: c})
	if err != nil {
		if errors.Is(err, openrfc.ErrTransport) || errors.Is(err, openrfc.ErrClosed) {
			s.dropSharedRFC(ctx)
		}
		msg := err.Error()
		if program != "" {
			msg = fmt.Sprintf("%s (temporary report %s)", msg, program)
		}
		if len(warnings) > 0 {
			msg += "\nWarnings:\n- " + strings.Join(warnings, "\n- ")
		}
		return newToolResultError(msg), nil
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func segmentOutcome(res *adt.SegmentResult, err error) (any, string, []string, error) {
	if res == nil {
		return nil, "", nil, err
	}
	return res, res.Program, res.Warnings, err
}

// segmentFields reads "fields": a list of {"name", "data_element",
// "iso_code"} objects, or of "NAME DATA_ELEMENT" strings.
func segmentFields(args map[string]any) ([]adt.SegmentField, bool, error) {
	raw, ok := args["fields"]
	if !ok || raw == nil {
		return nil, false, nil
	}
	if s, isString := raw.(string); isString {
		var decoded []any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			return nil, true, fmt.Errorf("fields must be a list: %v", err)
		}
		raw = decoded
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, true, fmt.Errorf("fields must be a list of {\"name\", \"data_element\"} objects")
	}
	fields := make([]adt.SegmentField, 0, len(list))
	for i, item := range list {
		switch v := item.(type) {
		case string:
			name, dtel, found := strings.Cut(strings.TrimSpace(v), " ")
			if !found {
				return nil, true, fmt.Errorf("field %d %q: write it as \"NAME DATA_ELEMENT\"", i+1, v)
			}
			fields = append(fields, adt.SegmentField{Name: name, DataElement: strings.TrimSpace(dtel)})
		case map[string]any:
			f := adt.SegmentField{Name: fmt.Sprint(v["name"]), DataElement: fmt.Sprint(v["data_element"])}
			if v["data_element"] == nil {
				f.DataElement = fmt.Sprint(v["rollname"])
			}
			if iso, ok := v["iso_code"].(bool); ok {
				f.ISOCode = iso
			}
			fields = append(fields, f)
		default:
			return nil, true, fmt.Errorf("field %d is neither an object nor a string", i+1)
		}
	}
	return fields, true, nil
}

// handleReadIDocSegment: SAP(action="read", target="SEGM Z1DEMO").
func (s *Server) handleReadIDocSegment(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	return s.withBackgroundRunner(ctx, args, "IDoc segments", func(r adt.BackgroundRunner) (any, string, []string, error) {
		seg, err := s.adtClient.ReadIDocSegment(ctx, getStringParam(args, "name"), r)
		if err != nil {
			return nil, "", nil, err
		}
		return seg, "", nil, nil
	})
}

// handleCreateIDocSegment: SAP(action="create", target="SEGM Z1DEMO",
// params={"description": "...", "fields": [...], "package": "...", "transport": "..."}).
func (s *Server) handleCreateIDocSegment(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	fields, _, err := segmentFields(args)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	o := adt.SegmentCreate{
		Name:        getStringParam(args, "name"),
		Description: getStringParam(args, "description"),
		Package:     getStringParam(args, "package"),
		Transport:   getStringParam(args, "transport"),
		Fields:      fields,
	}
	o.Qualified, _ = getBoolParam(args, "qualified")
	o.Release, _ = getBoolParam(args, "release")
	return s.withBackgroundRunner(ctx, args, "IDoc segments", func(r adt.BackgroundRunner) (any, string, []string, error) {
		return segmentOutcome(s.adtClient.CreateIDocSegment(ctx, o, r))
	})
}

// handleChangeIDocSegment: SAP(action="edit", target="SEGM Z1DEMO",
// params={"fields": [...complete list...], "release": true, "transport": "..."}).
func (s *Server) handleChangeIDocSegment(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	fields, given, err := segmentFields(args)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	o := adt.SegmentChange{Name: getStringParam(args, "name"), Transport: getStringParam(args, "transport")}
	if given {
		o.Fields = fields
		if o.Fields == nil {
			o.Fields = []adt.SegmentField{}
		}
	}
	if v, ok := getBoolParam(args, "release"); ok {
		o.Release = &v
	}
	return s.withBackgroundRunner(ctx, args, "IDoc segments", func(r adt.BackgroundRunner) (any, string, []string, error) {
		return segmentOutcome(s.adtClient.ChangeIDocSegment(ctx, o, r))
	})
}

// handleDeleteIDocSegment: SAP(action="delete", target="SEGM Z1DEMO", params={"transport": "..."}).
func (s *Server) handleDeleteIDocSegment(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	return s.withBackgroundRunner(ctx, args, "IDoc segments", func(r adt.BackgroundRunner) (any, string, []string, error) {
		return segmentOutcome(s.adtClient.DeleteIDocSegment(ctx, getStringParam(args, "name"), getStringParam(args, "transport"), r))
	})
}
