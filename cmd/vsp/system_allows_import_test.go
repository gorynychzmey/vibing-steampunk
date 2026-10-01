package main

import (
	"os"
	"path/filepath"
	"testing"
)

// allow_transport_import belongs to the system it is written for. A server
// without -s / SAP_SYSTEM takes it from the entry whose URL and client are its
// own, never from the default entry: pointed at another system by SAP_URL, it
// must not borrow the default system's permission to import.
func TestSystemAllowsImport_OnlyTheServersOwnEntry(t *testing.T) {
	dir := t.TempDir()
	const vspJSON = `{
  "default": "dev",
  "systems": {
    "dev":  {"url": "https://dev.example:44300", "client": "100", "allow_transport_import": true},
    "qas":  {"url": "https://qas.example:44300", "client": "100"},
    "bare": {"allow_transport_import": true}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, ".vsp.json"), []byte(vspJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("HOME", dir)

	saved := *cfg
	t.Cleanup(func() { *cfg = saved })

	for _, tc := range []struct {
		name, system, url, client string
		want                      bool
	}{
		{"named entry", "dev", "", "", true},
		{"named entry without the switch", "qas", "https://dev.example:44300", "100", false},
		{"URL and client match the default entry", "", "https://DEV.example:44300/", "100", true},
		{"SAP_URL names another system", "", "https://qas.example:44300", "100", false},
		{"SAP_URL names a system not in .vsp.json", "", "https://prd.example:44300", "100", false},
		{"same URL, other client", "", "https://dev.example:44300", "200", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.BaseURL, cfg.Client = tc.url, tc.client
			if got := systemAllowsImport(tc.system); got != tc.want {
				t.Errorf("systemAllowsImport(%q) with URL %q client %q = %v, want %v", tc.system, tc.url, tc.client, got, tc.want)
			}
		})
	}
}
