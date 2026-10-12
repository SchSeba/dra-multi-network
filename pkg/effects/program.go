// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

const (
	HostScope           = "host"
	SandboxScope        = "sandbox"
	StageScope          = "stage"
	Restored            = "Restored"
	Applied             = "Applied"
	InterfacePort       = "interface"
	Move                = "linux/link-move/v1"
	Configure           = "linux/link-configure/v1"
	CreateBond          = "linux/create-bond/v1"
	AttachMaster        = "linux/attach-master/v1"
	CreateVLAN          = "linux/create-vlan/v1"
	CreateMacvlan       = "linux/create-macvlan/v1"
	CreateIPvlan        = "linux/create-ipvlan/v1"
	GrantedInputs       = "allocation-and-owned-inputs"
	NativeStateMatch    = "native-state-match"
	ObserveAndReconcile = "ObserveAndReconcile"
)

const APIVersion = "effects.dra-multi-network.schseba.github.io/v1alpha1"

type Ref = topology.EffectRef
type Predicate = topology.EffectPredicate
type Recovery = topology.EffectRecovery
type Effect = topology.NetworkEffect
type Program = topology.EffectProgram

// The controller and node share these pure planners.
func ConnectNetwork(r api.RevisionSpec) (Program, error) { return topology.PlanConnect(r) }
func PrepareDevices(r api.RevisionSpec) (Program, error) { return topology.PlanPrepare(r) }
func Validate(p Program, budget []string) error          { return topology.ValidateProgram(p, budget) }
