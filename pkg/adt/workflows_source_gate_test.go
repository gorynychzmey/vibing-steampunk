package adt

import (
	"strings"
	"testing"
)

// An update names no package -- the object has one -- and the update paths
// check it through the object's URL. The early gate must not demand one.
func TestWriteSourceGate_UpdateWithAllowedPackages(t *testing.T) {
	cfg := NewConfig("https://sap.example.com", "developer", "secret")
	cfg.Safety.AllowedPackages = []string{"Z*", "$TMP"}
	c := NewClientWithTransport(cfg, NewTransportWithClient(cfg, &sequencedClient{t: t}))

	if err := c.writeSourceGate(&WriteSourceOptions{Mode: WriteModeUpdate}); err != nil {
		t.Errorf("an update was refused before its object was looked at: %v", err)
	}
	if err := c.writeSourceGate(&WriteSourceOptions{Mode: WriteModeCreate, Package: "ZDEMO"}); err != nil {
		t.Errorf("a create into an allowed package was refused: %v", err)
	}
	err := c.writeSourceGate(&WriteSourceOptions{Mode: WriteModeCreate, Package: "YOTHER"})
	if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "YOTHER") {
		t.Errorf("a create into a package outside the list was not refused: %v", err)
	}

	// Package is create-only metadata: a stale one on an update, or on an
	// upsert (whose create path gates the package itself), refuses nothing here.
	for _, mode := range []WriteSourceMode{WriteModeUpdate, WriteModeUpsert} {
		if err := c.writeSourceGate(&WriteSourceOptions{Mode: mode, Package: "YOTHER"}); err != nil {
			t.Errorf("%s with a stale package was refused before its object was looked at: %v", mode, err)
		}
	}

	cfg.Safety.ReadOnly = true
	if err := c.writeSourceGate(&WriteSourceOptions{Mode: WriteModeUpdate}); err == nil {
		t.Error("read-only mode did not refuse")
	}
}
