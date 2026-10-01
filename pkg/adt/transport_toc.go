package adt

import (
	"context"
	"errors"
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
	CopiedFrom []string `json:"copiedFrom,omitempty"`
	// CopiedEntries are the entries of the lists in CopiedFrom.
	CopiedEntries []string `json:"copiedEntries,omitempty"`
	// Failed are the lists TR_COPY_COMM did not copy, with the entries they
	// hold and why.
	Failed []TransportOfCopiesFailure `json:"failed,omitempty"`
	// Objects is the transport of copies' object list as read back after the
	// copy.
	Objects  []TransportObjectV2 `json:"objects,omitempty"`
	Released bool                `json:"released"`
	// ReleaseStatus says what happened to the release: "not requested",
	// "released", "not attempted: ..." or "failed: ...".
	ReleaseStatus string `json:"releaseStatus"`
}

// TransportOfCopiesFailure is one request or task whose list was not copied.
type TransportOfCopiesFailure struct {
	From    string   `json:"from"`
	Entries []string `json:"entries,omitempty"`
	Error   string   `json:"error"`
}

// incomplete is the error for a transport of copies that exists but is not
// what was asked for: what it holds, what is missing, and its release.
func (r *TransportOfCopiesResult) incomplete(what string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "transport of copies %s of %s is incomplete: %s", r.Transport, r.Source, what)
	if len(r.CopiedFrom) > 0 {
		fmt.Fprintf(&b, "; copied from %s: %s", strings.Join(r.CopiedFrom, ", "), strings.Join(r.CopiedEntries, ", "))
	} else {
		b.WriteString("; nothing was copied")
	}
	for _, f := range r.Failed {
		fmt.Fprintf(&b, "; not copied from %s (%s): %s", f.From, strings.Join(f.Entries, ", "), f.Error)
	}
	fmt.Fprintf(&b, "; release: %s", r.ReleaseStatus)
	return errors.New(b.String())
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
	if err := c.CheckTransportOfCopies(source); err != nil {
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

	res := &TransportOfCopiesResult{Source: source, Transport: number, Target: target, Description: description,
		ReleaseStatus: "not requested"}
	// Every list is tried, so the result says of each whether it was copied;
	// one that fails does not hide the state of the others.
	for _, src := range from {
		out, err := bridge.CallRFC(ctx, "TR_COPY_COMM", map[string]any{
			"WI_DIALOG":                "", // no dialog; an empty value turns the default 'X' off
			"WI_TRKORR_FROM":           src.Number,
			"WI_TRKORR_TO":             number,
			"WI_WITHOUT_DOCUMENTATION": "X",
		})
		if err == nil && out.Subrc != 0 {
			err = fmt.Errorf("sy-subrc %d%s", out.Subrc, withMessage(out.Message))
		}
		if err != nil {
			res.Failed = append(res.Failed, TransportOfCopiesFailure{From: src.Number, Entries: entryNames(src.Entries),
				Error: fmt.Sprintf("TR_COPY_COMM: %v", err)})
			continue
		}
		res.CopiedFrom = append(res.CopiedFrom, src.Number)
		res.CopiedEntries = append(res.CopiedEntries, entryNames(src.Entries)...)
	}
	if d, err := c.GetTransport(ctx, number); err == nil {
		res.Objects = d.Objects
	}
	if len(res.Failed) > 0 {
		if opts.Release {
			res.ReleaseStatus = "not attempted: the copy is incomplete"
		}
		return res, res.incomplete(fmt.Sprintf("%d of %d list(s) failed to copy", len(res.Failed), len(from)))
	}

	if opts.Release {
		// The copied objects stay locked in the original request, so a plain
		// release comes back asking whether to release anyway -- the question
		// SE01 asks too. For a transport of copies the answer is always yes.
		if err := c.ReleaseTransportV2(ctx, number, ReleaseTransportOptions{IgnoreLocks: true}); err != nil {
			res.ReleaseStatus = fmt.Sprintf("failed: %v", err)
			return res, res.incomplete("it is filled but was not released")
		}
		res.Released = true
		res.ReleaseStatus = "released"
	}
	return res, nil
}

// CheckTransportOfCopies runs the policy checks a transport of copies of
// source needs before anything is sent: transports enabled and not read-only
// (a new request is created), the operation allowed, and source readable
// under the transport whitelist. It sends nothing.
func (c *Client) CheckTransportOfCopies(source string) error {
	const op = "CopyToTransportOfCopies"
	if err := c.checkSafety(OpTransport, op); err != nil {
		return err
	}
	if err := c.config.Safety.CheckTransport("", op, true); err != nil {
		return err
	}
	return c.config.Safety.CheckTransport(strings.ToUpper(strings.TrimSpace(source)), op, false)
}

// copySource is one request or task whose object list TR_COPY_COMM copies,
// with the entries it holds.
type copySource struct {
	Number  string
	Entries []TransportObjectV2
}

// copySources names what TR_COPY_COMM has to copy from. It copies only the
// entries of the request it is given, not those of its tasks: a modifiable
// request keeps its objects in its tasks, so each task that holds some is
// copied, as SE01's include-objects does. Release moves the tasks' entries
// into the request, so a released one is copied from the request alone.
func copySources(d *TransportDetails) []copySource {
	if d.Status != "D" && d.Status != "L" {
		if len(d.Objects) > 0 {
			return []copySource{{Number: d.Number, Entries: d.Objects}}
		}
		return nil
	}
	var out []copySource
	for _, t := range d.Tasks {
		if len(t.Objects) > 0 {
			out = append(out, copySource{Number: t.Number, Entries: t.Objects})
		}
	}
	return out
}

// entryName is an entry as E071 keys it: "R3TR PROG ZDEMO".
func entryName(o TransportObjectV2) string {
	return strings.TrimSpace(o.PgmID + " " + o.Type + " " + o.Name)
}

func entryNames(objs []TransportObjectV2) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, entryName(o))
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
