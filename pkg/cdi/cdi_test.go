package cdi_test

import (
	"github.com/SchSeba/dra-multi-network/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
	"testing"
)

func TestScopedClaimDeviceIdentity(t *testing.T) {
	handler, err := cdi.NewHandler(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	edits := map[string]cdispec.ContainerEdits{"endpoint": {DeviceNodes: []*cdispec.DeviceNode{{Path: "/dev/null", HostPath: "/dev/null", Type: "c", Major: 1, Minor: 3, Permissions: "rw"}}}}
	if err := handler.CreateScopedSpecFile("claim", edits); err != nil {
		t.Fatal(err)
	}
	if err := handler.CheckScopedSpecFile("claim", edits); err != nil {
		t.Fatal(err)
	}
	edits["endpoint"].DeviceNodes[0].Minor = 7
	if err := handler.CheckScopedSpecFile("claim", edits); err == nil {
		t.Fatal("foreign device was accepted")
	}
	if err := handler.DeleteSpecFile("claim"); err != nil {
		t.Fatal(err)
	}
	if err := handler.CheckScopedSpecFile("claim", edits); err == nil {
		t.Fatal("removed claim spec was accepted")
	}
}
