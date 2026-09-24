package mcp

import (
	"context"
	"strings"
	"testing"
)

// Importing is off unless the system allows it -- independently of read-only,
// which is about the repository. The refusal comes before any connection.
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
