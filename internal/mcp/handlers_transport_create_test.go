package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestHandleCreateTransport_FilesTheRequestUnderACTSProject covers both ways a
// project reaches the request: named in the call, and configured on the server
// (--cts-project) for calls that name none.
func TestHandleCreateTransport_FilesTheRequestUnderACTSProject(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CSRF-Token", "TOKEN")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cts/transportrequests") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+
				`<tm:root xmlns:tm="http://www.sap.com/cts/adt/tm"><tm:request tm:number="TR-EXAMPLE"/></tm:root>`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	server := NewServer(&Config{
		BaseURL:          ts.URL,
		Username:         "u",
		Password:         "p",
		Client:           "001",
		Language:         "EN",
		EnableTransports: true,
		CTSProject:       "PRJ_DEFAULT",
		TransportTarget:  "/DEFAULT/",
	})

	cases := []struct {
		name          string
		args          map[string]any
		project, targ string
	}{
		{"configured default", map[string]any{"description": "demo", "package": "ZDEMO"}, "PRJ_DEFAULT", "/DEFAULT/"},
		{"named in the call", map[string]any{"description": "demo", "package": "ZDEMO",
			"cts_project": "PRJ_OTHER", "target": "/OTHER/"}, "PRJ_OTHER", "/OTHER/"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := server.handleCreateTransport(context.Background(), newRequest(tc.args))
			if err != nil || res.IsError {
				t.Fatalf("handleCreateTransport: err=%v result=%+v", err, res)
			}
			mu.Lock()
			body := bodies[i]
			mu.Unlock()
			for _, want := range []string{`tm:cts_project="` + tc.project + `"`, `tm:target="` + tc.targ + `"`} {
				if !strings.Contains(body, want) {
					t.Errorf("create body lacks %s\nbody: %s", want, body)
				}
			}
		})
	}
}

// Through SAP(action="system") "type" names the operation, so the request
// type comes as "request_type"; the expert-mode CreateTransport tool names it
// "type". A customizing request needs no package; an unknown type is refused
// instead of creating a workbench request.
func TestHandleCreateTransport_RequestType(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CSRF-Token", "TOKEN")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cts/transportrequests") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+
				`<tm:root xmlns:tm="http://www.sap.com/cts/adt/tm"><tm:request tm:number="TR-EXAMPLE"/></tm:root>`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	server := NewServer(&Config{BaseURL: ts.URL, Username: "u", Password: "p", Client: "001", Language: "EN", EnableTransports: true})

	cases := []struct {
		name     string
		args     map[string]any
		wantType string // "" = refused
		wantErr  string
	}{
		{"system action, customizing", map[string]any{"type": "create_transport", "request_type": "customizing", "description": "demo"}, "W", ""},
		{"system action, default workbench", map[string]any{"type": "create_transport", "description": "demo", "package": "ZDEMO"}, "K", ""},
		{"expert tool names it type", map[string]any{"type": "customizing", "description": "demo"}, "W", ""},
		{"letter W", map[string]any{"type": "create_transport", "request_type": "W", "description": "demo"}, "W", ""},
		{"workbench still needs a package", map[string]any{"type": "create_transport", "description": "demo"}, "", "package is required"},
		{"unknown type is refused", map[string]any{"type": "create_transport", "request_type": "transport of copies", "description": "demo"}, "", "unknown request type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			before := len(bodies)
			mu.Unlock()
			res, err := server.handleCreateTransport(context.Background(), newRequest(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			sent := bodies[before:]
			mu.Unlock()
			if tc.wantType == "" {
				if !res.IsError || !strings.Contains(res.Content[0].(mcp.TextContent).Text, tc.wantErr) || len(sent) != 0 {
					t.Errorf("want a refusal naming %q and nothing sent; got %+v, %d request(s)", tc.wantErr, res, len(sent))
				}
				return
			}
			if res.IsError || len(sent) != 1 || !strings.Contains(sent[0], `tm:type="`+tc.wantType+`"`) {
				t.Errorf("want one request of type %s; got %+v, bodies %v", tc.wantType, res, sent)
			}
		})
	}
}
