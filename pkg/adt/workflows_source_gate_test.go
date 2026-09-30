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

	cfg.Safety.ReadOnly = true
	if err := c.writeSourceGate(&WriteSourceOptions{Mode: WriteModeUpdate}); err == nil {
		t.Error("read-only mode did not refuse")
	}
}
