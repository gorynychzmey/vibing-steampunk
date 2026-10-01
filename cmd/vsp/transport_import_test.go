package main

import (
	"strings"
	"testing"
)

// The CLI import applies the same safety switches as the MCP server.
func TestCheckImportAllowed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params systemParams
		want   string
	}{
		{"off", systemParams{}, "allow_transport_import"},
		{"allowed", systemParams{AllowTransportImport: true}, ""},
		{"read-only", systemParams{AllowTransportImport: true, ReadOnly: true}, "read-only"},
		{"transport read-only", systemParams{AllowTransportImport: true, TransportReadOnly: true}, "transport read-only"},
		{"outside allowed transports", systemParams{AllowTransportImport: true, AllowedTransports: []string{"A4HK*"}}, "DEVK900001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkImportAllowed(&tc.params, []string{"DEVK900001"})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want allowed, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %q, got %v", tc.want, err)
			}
		})
	}
}

// --client is taken only when it names the system's own client.
func TestImportClient(t *testing.T) {
	if got, err := importClient("", "100"); err != nil || got != "100" {
		t.Errorf("no flag: %q, %v", got, err)
	}
	if got, err := importClient("100", "100"); err != nil || got != "100" {
		t.Errorf("own client: %q, %v", got, err)
	}
	if _, err := importClient("200", "100"); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("another client: %v, want a refusal", err)
	}
}
