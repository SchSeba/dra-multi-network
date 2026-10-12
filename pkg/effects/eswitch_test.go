// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"errors"
	"github.com/vishvananda/netlink"
	"testing"
)

func TestEswitchPersistedTransitionAndRestoration(t *testing.T) {
	before := EswitchSnapshot{PCI: "0000:29:00.0", Attrs: netlink.DevlinkDevEswitchAttr{Mode: "legacy"}, VFCount: 1, Functions: []EswitchFunction{{PCI: "0000:29:00.0", Driver: "test"}}}
	s := EswitchState{Grant: EswitchGrant{PCI: before.PCI, PolicyUID: "policy", Profile: EswitchProfile, AllowedModes: []string{"legacy", "switchdev"}}, Before: before, Target: "switchdev"}
	saved, calls := 0, 0
	save := func() error { saved++; return nil }
	set := func(mode string) error {
		calls++
		if saved == 0 || mode != s.Target {
			t.Fatal("setter preceded durable intent")
		}
		return errors.New("lost acknowledgement")
	}
	if err := issueEswitch(&s, before, false, save, set); err == nil || !s.Attempted || calls != 1 {
		t.Fatal("missing transition intent", err, s)
	}
	if err := issueEswitch(&s, before, false, save, set); err == nil || calls != 1 {
		t.Fatal("uncertain transition repeated", err)
	}
	current := before
	current.Attrs.Mode = "switchdev"
	if err := issueEswitch(&s, current, false, save, set); err != nil || !s.Completed || calls != 1 {
		t.Fatal("lost ack did not reconcile", err)
	}
	changed := current
	changed.Functions = []EswitchFunction{{PCI: before.PCI, Driver: "foreign"}}
	if err := issueEswitch(&s, changed, true, save, set); err == nil {
		t.Fatal("foreign domain restored")
	}
	if err := issueEswitch(&s, current, true, save, func(mode string) error {
		calls++
		if !s.RestoreAttempted || mode != "legacy" {
			t.Fatal("restore intent")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := issueEswitch(&s, before, true, save, set); err != nil || !s.Restored || calls != 2 {
		t.Fatal("restore observation", err)
	}
}
