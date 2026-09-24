package adt

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// A transport of copies (request type T) carries a copy of another request's
// object list to a target system, to test a change there before the original
// request is released. SE01 builds one in two steps: create the request, then
// "include objects" from the original, which is TR_COPY_COMM. ADT creates the
// request -- tm:type T with a target -- but has no resource for the copy, and
// TR_COPY_COMM is not remote-enabled, so the copy goes through ZADT_VSP's
// function bridge, like MergeTransports.

// functionBridge is the part of ZADT_VSP's WebSocket client a copy needs.
type functionBridge interface {
	CallRFC(ctx context.Context, function string, params map[string]any) (*RFCResult, error)
}

// TransportOfCopiesOptions says where a transport of copies goes.
type TransportOfCopiesOptions struct {
	// Target is the system (QAS) or target group (/GROUP/) the copy is for.
	// ADT takes a system without a client; SE01's QAS.100 form is refused.
	Target string
	// Description defaults to "ToC " and the original request's.
	Description string
	// CTSProject defaults to the configured one (WithCTSProject). Systems that
	// require a project refuse an ADT-created request without one, a
	// transport of copies included.
	CTSProject string
	// Release releases the transport of copies once it is filled.
	Release bool
}

// TransportOfCopiesResult is what a copy did.
type TransportOfCopiesResult struct {
	Source      string `json:"source"`
	Transport   string `json:"transport,omitempty"`
	Target      string `json:"target"`
	Description string `json:"description"`
	// CopiedFrom are the requests or tasks whose object lists were copied.
	CopiedFrom []string            `json:"copiedFrom,omitempty"`
	Objects    []TransportObjectV2 `json:"objects,omitempty"`
	Released   bool                `json:"released"`
}

// CopyToTransportOfCopies creates a transport of copies for target and copies
// source's object list into it, releasing it when asked. It needs ZADT_VSP's
// function bridge, and checks for it before anything is created.
func (c *Client) CopyToTransportOfCopies(ctx context.Context, ws *DebugWebSocketClient, source string, opts TransportOfCopiesOptions) (*TransportOfCopiesResult, error) {
	if ws == nil || !ws.IsConnected() {
		return nil, fmt.Errorf("copying a request's objects needs ZADT_VSP's function bridge (a WebSocket to the system)")
	}
	return c.copyToTransportOfCopies(ctx, ws, source, opts)
}

func (c *Client) copyToTransportOfCopies(ctx context.Context, bridge functionBridge, source string, opts TransportOfCopiesOptions) (*TransportOfCopiesResult, error) {
	source = strings.ToUpper(strings.TrimSpace(source))
	target := strings.TrimSpace(opts.Target)
	if source == "" {
		return nil, fmt.Errorf("the request to copy is required")
	}
	if target == "" {
		return nil, fmt.Errorf("a target is required: the system (QAS) or target group (/GROUP/) the copy is for")
	}
	if err := c.config.Safety.CheckTransport(source, "CopyToTransportOfCopies", false); err != nil {
		return nil, err
	}
	if err := c.config.Safety.CheckTransport("", "CopyToTransportOfCopies", true); err != nil {
		return nil, err
	}

	details, err := c.GetTransport(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", source, err)
	}
	from := copySources(details)
	if len(from) == 0 {
		return nil, fmt.Errorf("%s holds no objects to copy", source)
	}

	description := opts.Description
	if description == "" {
		description = transportOfCopiesDescription(details.Description)
	}
	resp, err := c.transport.Request(ctx, "/sap/bc/adt/cts/transportrequests", &RequestOptions{
		Method:      http.MethodPost,
		Body:        []byte(c.newRequestBody("T", description, target, opts.CTSProject, false)),
		ContentType: acceptTransportOrganizerV1,
		Accept:      acceptTransportOrganizerV1,
	})
	if err != nil {
		return nil, fmt.Errorf("creating the transport of copies: %w", err)
	}
	number, err := parseCreateTransportResponse(resp.Body)
	if err != nil {
		return nil, err
	}

	res := &TransportOfCopiesResult{Source: source, Transport: number, Target: target, Description: description}
	for _, n := range from {
		out, err := bridge.CallRFC(ctx, "TR_COPY_COMM", map[string]any{
			"WI_DIALOG":                "", // no dialog; an empty value turns the default 'X' off
			"WI_TRKORR_FROM":           n,
			"WI_TRKORR_TO":             number,
			"WI_WITHOUT_DOCUMENTATION": "X",
		})
		if err == nil && out.Subrc != 0 {
			err = fmt.Errorf("sy-subrc %d%s", out.Subrc, withMessage(out.Message))
		}
		if err != nil {
			return res, fmt.Errorf("TR_COPY_COMM %s into %s: %v; %s exists, not released, holding what was copied before",
				n, number, err, number)
		}
		res.CopiedFrom = append(res.CopiedFrom, n)
	}
	if d, err := c.GetTransport(ctx, number); err == nil {
		res.Objects = d.Objects
	}

	if opts.Release {
		// The copied objects stay locked in the original request, so a plain
		// release comes back asking whether to release anyway -- the question
		// SE01 asks too. For a transport of copies the answer is always yes.
		if err := c.ReleaseTransportV2(ctx, number, ReleaseTransportOptions{IgnoreLocks: true}); err != nil {
			return res, fmt.Errorf("%s is filled but was not released: %w", number, err)
		}
		res.Released = true
	}
	return res, nil
}

// copySources names what TR_COPY_COMM has to copy from. It copies only the
// entries of the request it is given, not those of its tasks: a modifiable
// request keeps its objects in its tasks, so each task that holds some is
// copied, as SE01's include-objects does. Release moves the tasks' entries
// into the request, so a released one is copied from the request alone.
func copySources(d *TransportDetails) []string {
	if d.Status != "D" && d.Status != "L" {
		if len(d.Objects) > 0 {
			return []string{d.Number}
		}
		return nil
	}
	var out []string
	for _, t := range d.Tasks {
		if len(t.Objects) > 0 {
			out = append(out, t.Number)
		}
	}
	return out
}

// transportOfCopiesDescription is "ToC " and the original's description, cut
// to the 60 characters E07T holds.
func transportOfCopiesDescription(original string) string {
	d := []rune("ToC " + strings.TrimSpace(original))
	if len(d) > 60 {
		d = d[:60]
	}
	return string(d)
}
