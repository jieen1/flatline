package main

import (
	"strings"
	"testing"
)

// The archive promise needs the rescuer running: harnesses delete transcripts
// after 30 days whether or not the daemon was up. The unit keeps the daemon
// alive across reboots; asset scope is user, because a service has no project
// working directory and must not register $HOME as one.
func TestServiceUnitKeepsTheDaemonAliveWithUserScope(t *testing.T) {
	unit := serviceUnit("/home/x/bin/flatline")
	for _, want := range []string{
		"ExecStart=/home/x/bin/flatline daemon -asset-scope user",
		"Restart=on-failure",
		"WantedBy=default.target",
		"Description=",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit misses %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "-asset-root") {
		t.Error("the unit must not pin an asset root; the daemon resolves user roots itself")
	}
}
