package mcp

import (
	"context"
	"strings"
	"testing"
)

// Importing is off unless the system allows it. The refusal comes before any
// connection.
func TestHandleImportTransport_IsOffUnlessAllowed(t *testing.T) {
	server := NewServer(&Config{
		BaseURL: "https://unreachable.invalid:44300", Username: "u", Password: "p", Client: "100",
	})
	res, err := server.handleImportTransport(context.Background(), newRequest(map[string]any{"transport": "TR-EXAMPLE"}))
	if err != nil {
		t.Fatalf("handleImportTransport: %v", err)
	}
	if !res.IsError {
		t.Fatal("imported without allow_transport_import")
	}
	if text := resultText(res); !strings.Contains(text, "allow_transport_import") {
		t.Errorf("the refusal does not say how to allow it: %s", text)
	}
}

func TestHandleImportTransport_NeedsARequest(t *testing.T) {
	server := NewServer(&Config{
		BaseURL: "https://unreachable.invalid:44300", Username: "u", Password: "p", Client: "100",
		AllowTransportImport: true,
	})
	res, _ := server.handleImportTransport(context.Background(), newRequest(map[string]any{}))
	if !res.IsError || !strings.Contains(resultText(res), "transport") {
		t.Errorf("want a refusal naming transport, got %+v", res)
	}
}

func TestTransportList(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want int
	}{
		{"TR-A", 1}, {"TR-A, TR-B", 2}, {[]any{"TR-A", " ", "TR-B"}, 2}, {nil, 0},
	} {
		if got := transportList(tc.in); len(got) != tc.want {
			t.Errorf("transportList(%v) = %v, want %d entries", tc.in, got, tc.want)
		}
	}
}

// The import's own switch does not override the others: read-only, transport
// read-only and the allowed transports refuse it before any connection.
func TestHandleImportTransport_FailsClosedUnderTheSafetySwitches(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"read-only", Config{ReadOnly: true}, "read-only"},
		{"transport read-only", Config{TransportReadOnly: true}, "transport read-only"},
		{"outside allowed transports", Config{AllowedTransports: []string{"A4HK*"}}, "TR-EXAMPLE"},
		{"workflow ops disallowed", Config{DisallowedOps: "W"}, "TransportImport"},
		{"workflow ops not in allowed ops", Config{AllowedOps: "RSQ"}, "TransportImport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.BaseURL, cfg.Username, cfg.Password, cfg.Client = "https://unreachable.invalid:44300", "u", "p", "100"
			cfg.AllowTransportImport = true
			server := NewServer(&cfg)
			res, err := server.handleImportTransport(context.Background(), newRequest(map[string]any{"transport": "TR-EXAMPLE"}))
			if err != nil {
				t.Fatalf("handleImportTransport: %v", err)
			}
			text := resultText(res)
			if !res.IsError || !strings.Contains(text, "blocked") || !strings.Contains(text, tc.want) {
				t.Errorf("want a refusal naming %q, got %s", tc.want, text)
			}
		})
	}
}

// A per-call gateway, system number, port or user would send the import to
// another system than the one the switch was enabled for; each is refused
// before any connection.
func TestHandleImportTransport_RefusesDestinationOverrides(t *testing.T) {
	server := NewServer(&Config{
		BaseURL: "https://unreachable.invalid:44300", Username: "u", Password: "p", Client: "100",
		AllowTransportImport: true,
	})
	for key, val := range map[string]any{"host": "other.invalid", "sysnr": "01", "port": float64(3301), "user": "SOMEONE"} {
		t.Run(key, func(t *testing.T) {
			res, err := server.handleImportTransport(context.Background(),
				newRequest(map[string]any{"transport": "TR-EXAMPLE", key: val}))
			if err != nil {
				t.Fatalf("handleImportTransport: %v", err)
			}
			text := resultText(res)
			if !res.IsError || !strings.Contains(text, "does not take") || !strings.Contains(text, key) {
				t.Errorf("want a refusal of %q, got %s", key, text)
			}
		})
	}
}
