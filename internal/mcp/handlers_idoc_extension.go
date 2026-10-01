package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/oisee/vibing-steampunk/pkg/adt"
)

// routeIDocExtensionAction routes IDoc extension types (WE30):
// read/create/edit/delete target="IEXT <name>".
func (s *Server) routeIDocExtensionAction(ctx context.Context, action, objectType, objectName string, params map[string]any) (*mcp.CallToolResult, bool, error) {
	if objectType != "IEXT" {
		return nil, false, nil
	}
	params = withName(params, objectName)
	switch action {
	case "read":
		return s.callHandler(ctx, s.handleReadIDocExtension, params)
	case "create":
		return s.callHandler(ctx, s.handleCreateIDocExtension, params)
	case "edit":
		return s.callHandler(ctx, s.handleChangeIDocExtension, params)
	case "delete":
		return s.callHandler(ctx, s.handleDeleteIDocExtension, params)
	}
	return nil, false, nil
}

func extensionOutcome(res *adt.ExtensionResult, err error) (any, string, []string, error) {
	if res == nil {
		return nil, "", nil, err
	}
	return res, res.Program, res.Warnings, err
}

// extensionSegments reads "segments": a list of {"segment", "parent", "min",
// "max", "mandatory"} objects, or of "SEGMENT PARENT" strings (1..1).
func extensionSegments(args map[string]any) ([]adt.ExtensionSegment, bool, error) {
	raw, ok := args["segments"]
	if !ok || raw == nil {
		return nil, false, nil
	}
	if s, isString := raw.(string); isString {
		var decoded []any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			return nil, true, fmt.Errorf("segments must be a list: %v", err)
		}
		raw = decoded
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, true, fmt.Errorf("segments must be a list of {\"segment\", \"parent\"} objects")
	}
	segs := make([]adt.ExtensionSegment, 0, len(list))
	for i, item := range list {
		switch v := item.(type) {
		case string:
			seg, parent, found := strings.Cut(strings.TrimSpace(v), " ")
			if !found {
				return nil, true, fmt.Errorf("segment %d %q: write it as \"SEGMENT PARENT\"", i+1, v)
			}
			segs = append(segs, adt.ExtensionSegment{Segment: seg, Parent: strings.TrimSpace(parent)})
		case map[string]any:
			s := adt.ExtensionSegment{}
			s.Segment, _ = v["segment"].(string)
			s.Parent, _ = v["parent"].(string)
			if n, ok := v["min"].(float64); ok {
				s.Min = int64(n)
			}
			if n, ok := v["max"].(float64); ok {
				s.Max = int64(n)
			}
			s.Mandatory, _ = v["mandatory"].(bool)
			if s.Segment == "" || s.Parent == "" {
				return nil, true, fmt.Errorf("segment %d needs \"segment\" and \"parent\"", i+1)
			}
			segs = append(segs, s)
		default:
			return nil, true, fmt.Errorf("segment %d is neither an object nor a string", i+1)
		}
	}
	return segs, true, nil
}

// handleReadIDocExtension: SAP(action="read", target="IEXT ZDEMO01").
func (s *Server) handleReadIDocExtension(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	return s.withBackgroundRunner(ctx, args, func(r adt.BackgroundRunner) (any, string, []string, error) {
		ext, err := s.adtClient.ReadIDocExtension(ctx, getStringParam(args, "name"), r)
		if err != nil {
			return nil, "", nil, err
		}
		return ext, "", nil, nil
	})
}

// handleCreateIDocExtension: SAP(action="create", target="IEXT ZDEMO01",
// params={"basic_type": "DELVRY07", "description": "...", "segments": [...], "package": "...", "transport": "..."}).
func (s *Server) handleCreateIDocExtension(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	segs, _, err := extensionSegments(args)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	o := adt.ExtensionCreate{
		Name:        getStringParam(args, "name"),
		BasicType:   firstNonEmptyArg(args, "basic_type", "idoctype"),
		Description: getStringParam(args, "description"),
		Package:     getStringParam(args, "package"),
		Transport:   getStringParam(args, "transport"),
		Segments:    segs,
	}
	o.Release, _ = getBoolParam(args, "release")
	return s.withBackgroundRunner(ctx, args, func(r adt.BackgroundRunner) (any, string, []string, error) {
		return extensionOutcome(s.adtClient.CreateIDocExtension(ctx, o, r))
	})
}

// handleChangeIDocExtension: SAP(action="edit", target="IEXT ZDEMO01",
// params={"segments": [...complete list...], "description": "...", "release": true, "transport": "..."}).
func (s *Server) handleChangeIDocExtension(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	segs, given, err := extensionSegments(args)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	o := adt.ExtensionChange{
		Name:        getStringParam(args, "name"),
		Transport:   getStringParam(args, "transport"),
		Description: getStringParam(args, "description"),
	}
	if given {
		o.Segments = segs
		if o.Segments == nil {
			o.Segments = []adt.ExtensionSegment{}
		}
	}
	if v, ok := getBoolParam(args, "release"); ok {
		o.Release = &v
	}
	return s.withBackgroundRunner(ctx, args, func(r adt.BackgroundRunner) (any, string, []string, error) {
		return extensionOutcome(s.adtClient.ChangeIDocExtension(ctx, o, r))
	})
}

// handleDeleteIDocExtension: SAP(action="delete", target="IEXT ZDEMO01", params={"transport": "..."}).
func (s *Server) handleDeleteIDocExtension(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	return s.withBackgroundRunner(ctx, args, func(r adt.BackgroundRunner) (any, string, []string, error) {
		return extensionOutcome(s.adtClient.DeleteIDocExtension(ctx, getStringParam(args, "name"), getStringParam(args, "transport"), r))
	})
}

func firstNonEmptyArg(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(getStringParam(args, k)); v != "" {
			return v
		}
	}
	return ""
}
