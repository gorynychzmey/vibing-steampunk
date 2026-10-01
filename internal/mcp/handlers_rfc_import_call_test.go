package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// op="call" on an import function module must not skip the import's gate:
// with the import switch off it is refused before any gateway connection, and
// with it on it is still refused in favour of import_transport, which pins the
// server's own system.
func TestRFCCall_ImportFunctionModulesRefusedBeforeLogon(t *testing.T) {
	for _, tc := range []struct {
		name, fm  string
		allow     bool
		extra     map[string]any
		wantInErr string
	}{
		{"switch off", "CTS_API_IMPORT_CHANGE_REQUEST", false, map[string]any{"op": "call"}, "allow_transport_import"},
		{"switch off, op implied by args", "cts_api_import_change_request", false,
			map[string]any{"args": map[string]any{"SYSTEM": "PRD", "CLIENT": "100"}}, "allow_transport_import"},
		{"switch off, TMS manager", "TMS_MGR_IMPORT_TR_REQUEST", false, map[string]any{"op": "call"}, "allow_transport_import"},
		{"switch off, tp import", "TMS_TP_IMPORT", false, map[string]any{"op": "call"}, "allow_transport_import"},
		{"switch off, any TMS_*IMPORT*", "TMS_UI_IMPORT_TR_REQUEST", false, map[string]any{"op": "call"}, "allow_transport_import"},
		{"switch on", "CTS_API_IMPORT_CHANGE_REQUEST", true, map[string]any{"op": "call"}, "import_transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep a developer's own .vsp.json out of the destination.
			t.Setenv("HOME", t.TempDir())
			t.Chdir(t.TempDir())
			s := NewServer(&Config{
				BaseURL: "http://127.0.0.1:1", Username: "u", Password: "p", Client: "001", Language: "EN",
				AllowTransportImport: tc.allow,
			})
			port, dials := fakeGateway(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, handled, err := s.routeRFCAction(ctx, "rfc", tc.fm, "", rfcParams(port, tc.extra))
			if !handled {
				t.Fatal("rfc action not handled")
			}
			if err == nil || !strings.Contains(err.Error(), "is blocked") || !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("want a refusal naming %q, got %v", tc.wantInErr, err)
			}
			if n := waitDials(dials, 1, 200*time.Millisecond); n != 0 {
				t.Errorf("refused import call still dialled the gateway %d time(s)", n)
			}
		})
	}
}

func TestIsTransportImportFM(t *testing.T) {
	for fm, want := range map[string]bool{
		"CTS_API_IMPORT_CHANGE_REQUEST": true,
		" tms_tp_import ":               true,
		"TMS_MGR_IMPORT_TR_REQUEST":     true,
		"TMS_UI_IMPORT_TR_REQUEST":      true,
		"Z_DOUBLE":                      false,
		"STFC_CONNECTION":               false,
		"TMS_MGR_READ_TRANSPORT_QUEUE":  false,
	} {
		if got := isTransportImportFM(fm); got != want {
			t.Errorf("isTransportImportFM(%q) = %v, want %v", fm, got, want)
		}
	}
}
