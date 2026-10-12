// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestOfflinePreviewUsesControllerCompilerAndRejectsPartialResults(t *testing.T) {
	raw, err := os.ReadFile("../../examples/bond-vlan/topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"-"}, bytes.NewReader(raw), &output); err != nil {
		t.Fatal(err)
	}
	var preview struct {
		Items []api.NetworkTopologyRevision `json:"items"`
	}
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 1 || preview.Items[0].Annotations[api.Group+"/offline-preview"] != "true" {
		t.Fatalf("expected one explicitly marked preview: %+v", preview)
	}
	if err := topology.VerifyRevision(preview.Items[0].Spec); err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{
		{"../../examples/effect-program/plugin.yaml", "../../examples/effect-program/topology.yaml"},
		{"../../examples/root-effect-program/topology.yaml", "../../examples/root-effect-program/plugin.yaml"},
	} {
		output.Reset()
		if err := run(paths, strings.NewReader(""), &output); err != nil {
			t.Fatalf("custom descriptors must compile regardless of file order: %v", err)
		}
		preview.Items = nil
		if err := json.Unmarshal(output.Bytes(), &preview); err != nil {
			t.Fatal(err)
		}
		if err := topology.VerifyRevision(preview.Items[0].Spec); err != nil {
			t.Fatal(err)
		}
	}
	// All failures must leave stdout empty, including a valid topology followed
	// by an invalid document or a second invalid topology.
	invalid := strings.Replace(string(raw), "dual-vf-bond-vlan", "invalid", 1)
	invalid = strings.Replace(invalid, "sriov-cni", "unknown-plugin", 1)
	for _, input := range []string{
		string(raw) + "\n---\n" + string(raw),
		string(raw) + "\n---\n" + invalid,
		string(raw) + "\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: unsupported\n",
		string(raw) + "\n---\napiVersion: [invalid\n",
		strings.Replace(string(raw), "spec:\n", "unknownField: true\nspec:\n", 1),
	} {
		output.Reset()
		if err := run([]string{"-"}, strings.NewReader(input), &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid input produced a partial/successful preview: err=%v bytes=%d", err, output.Len())
		}
	}
	output.Reset()
	if err := run(nil, strings.NewReader(""), &output); err == nil {
		t.Fatal("missing inputs accepted")
	}
	if err := run([]string{"does-not-exist"}, strings.NewReader(""), &output); err == nil {
		t.Fatal("missing file accepted")
	}
}
