// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"os"
	"strings"
	"testing"

	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
)

func TestIPAMTopologiesAndLeaseScope(t *testing.T) {
	for _, file := range []string{"../../examples/ipam/topology.yaml", "../../examples/ipam/two-vf/topology.yaml"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var source api.NetworkTopology
		if err := yaml.UnmarshalStrict(data, &source); err != nil {
			t.Fatal(err)
		}
		revision, err := Compile(source, PluginRegistry(nil))
		if err != nil {
			t.Fatal(file, err)
		}
		if err := VerifyRevision(revision); err != nil {
			t.Fatal(err)
		}
		program, err := PlanConnect(revision)
		if err != nil {
			t.Fatal(err)
		}
		addresses := 0
		for _, effect := range program.Effects {
			if effect.Kind == AddressConfigure {
				addresses++
			}
		}
		expected := 1
		if strings.Contains(file, "two-vf") {
			expected = 2
		}
		if addresses != expected {
			t.Fatalf("%s: %d address effects", file, addresses)
		}
	}
	data, _ := os.ReadFile("../../examples/ipam/two-vf/topology.yaml")
	var source api.NetworkTopology
	if err := yaml.UnmarshalStrict(data, &source); err != nil {
		t.Fatal(err)
	}
	for i := range source.Spec.Steps {
		if source.Spec.Steps[i].ID == "configure1" {
			source.Spec.Steps[i].Inputs[LeasePort] = ext.JSON{Raw: []byte(`{"from":"address0.lease"}`)}
		}
	}
	if _, err := Compile(source, PluginRegistry(nil)); err == nil || !strings.Contains(err.Error(), "E_LEASE_DUPLICATE") {
		t.Fatal("one IP allowed on two interfaces", err)
	}
}
