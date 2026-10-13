// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/pluginapi"
)

const TuningPlugin = "tuning-cni"
const IPv4Forwarding = pluginapi.IPv4Forwarding

//nolint:goconst // JSON Schema and canonical descriptor vocabulary remains literal.
func tuningPlugin() api.NetworkPlugin {
	return api.NetworkPlugin{
		TypeMeta:   metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkPlugin"},
		ObjectMeta: metav1.ObjectMeta{Name: TuningPlugin},
		Spec: api.PluginSpec{
			Implementation: api.Implementation{Type: BuiltinImplementation, Name: TuningPlugin, Version: ContractVersion},
			Kind:           OperationKind,
			StateContract:  api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects},
			ConfigSchema:   schema(map[string]any{"ipv4Forward": map[string]any{"type": SchemaBoolean}}, []string{"ipv4Forward"}),
			Inputs:         map[string]api.Port{InterfacePort: {Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Access: AccessOwned}},
			Outputs:        map[string]api.Port{"configured": {Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}},
			AllowedEffects: []string{IPv4Forwarding},
		},
	}
}
