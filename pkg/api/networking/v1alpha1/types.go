// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import (
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const Group = "networking.dra-multi-network.schseba.github.io"
const Driver = "dra-multi-network.schseba.github.io"

var GroupVersion = schema.GroupVersion{Group: Group, Version: "v1alpha1"}
var SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &DeviceExposurePolicy{}, &DeviceExposurePolicyList{}, &NetworkPlugin{}, &NetworkPluginList{}, &NetworkTopology{}, &NetworkTopologyList{}, &NetworkTopologyRevision{}, &NetworkTopologyRevisionList{}, &NetworkAttachmentStatus{}, &NetworkAttachmentStatusList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
})
var AddToScheme = SchemeBuilder.AddToScheme

type EffectBudget struct {
	// +kubebuilder:validation:MaxItems=32
	Allow []string `json:"allow"`
}
type PluginRef struct {
	Name string `json:"name"`
}
type Reference struct {
	From string `json:"from"`
}
type Step struct {
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,39}$`
	ID string `json:"id"`
	// +kubebuilder:validation:Enum=Allocation;Operation
	Kind      string    `json:"kind"`
	PluginRef PluginRef `json:"pluginRef"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Config apiext.JSON `json:"config"`
	// Inputs contain a reference object or an array of reference objects per port.
	// +kubebuilder:pruning:PreserveUnknownFields
	Inputs map[string]apiext.JSON `json:"inputs,omitempty"`
	// Administrator CEL selectors are intersected with mandatory persona selectors.
	// +kubebuilder:validation:MaxItems=8
	Selectors []string `json:"selectors,omitempty"`
}
type TopologySpec struct {
	EffectBudget EffectBudget `json:"effectBudget"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=id
	Steps []Step `json:"steps"`
}
type RevisionRef struct {
	Name   string `json:"name"`
	UID    string `json:"uid"`
	Digest string `json:"digest"`
}
type RootClass struct {
	RootStep  string `json:"rootStep"`
	ClassName string `json:"className"`
}
type TopologyStatus struct {
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	RevisionRef        *RevisionRef `json:"revisionRef,omitempty"`
	// +listType=map
	// +listMapKey=rootStep
	RootClasses []RootClass `json:"rootClasses,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ntop
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.revisionRef.name`
type NetworkTopology struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TopologySpec   `json:"spec"`
	Status            TopologyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NetworkTopologyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkTopology `json:"items"`
}

type Port struct {
	Type      string `json:"type"`
	LinkType  string `json:"linkType,omitempty"`
	MinItems  int    `json:"minItems"`
	MaxItems  int    `json:"maxItems"`
	Access    string `json:"access,omitempty"`
	Ownership string `json:"ownership,omitempty"`
}
type Implementation struct {
	// +kubebuilder:validation:Enum=Builtin;EffectProgram
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version"`
}
type StateContract struct {
	Version string `json:"version"`
	Cleanup string `json:"cleanup"`
}
type AllocationProfileRef struct {
	Name string `json:"name"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	Digest string `json:"digest"`
}
type PluginSpec struct {
	Implementation Implementation `json:"implementation"`
	// +kubebuilder:validation:Enum=Allocation;Operation
	Kind string `json:"kind"`
	// +kubebuilder:pruning:PreserveUnknownFields
	ConfigSchema   apiext.JSON     `json:"configSchema"`
	Inputs         map[string]Port `json:"inputs,omitempty"`
	Outputs        map[string]Port `json:"outputs"`
	AllowedEffects []string        `json:"allowedEffects"`
	StateContract  StateContract   `json:"stateContract"`
	// Programmable roots retain a certified allocation/ownership contract.
	AllocationProfileRef *AllocationProfileRef `json:"allocationProfileRef,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Hooks map[string]apiext.JSON `json:"hooks,omitempty"`
}
type PluginStatus struct {
	Digest             string             `json:"digest,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type NetworkPlugin struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PluginSpec   `json:"spec"`
	Status            PluginStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NetworkPluginList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkPlugin `json:"items"`
}

type LockedPlugin struct {
	Name   string     `json:"name"`
	Digest string     `json:"digest"`
	Spec   PluginSpec `json:"spec"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="revision spec is immutable"
type RevisionSpec struct {
	SourceUID       string       `json:"sourceUID"`
	Digest          string       `json:"digest"`
	CompilerVersion string       `json:"compilerVersion"`
	Topology        TopologySpec `json:"topology"`
	// +kubebuilder:validation:MaxItems=32
	Plugins []LockedPlugin `json:"plugins"`
	Order   []string       `json:"order"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=nrev
// +kubebuilder:subresource:status
type NetworkTopologyRevision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RevisionSpec   `json:"spec"`
	Status            TopologyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NetworkTopologyRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkTopologyRevision `json:"items"`
}

type DeviceSelector struct {
	// +kubebuilder:validation:Enum=PF;VF;PCI
	Kind string `json:"kind"`
	// +kubebuilder:validation:MaxItems=128
	PCIAddresses       []string `json:"pciAddresses,omitempty"`
	ParentPCIAddresses []string `json:"parentPCIAddresses,omitempty"`
	Vendor             string   `json:"vendor,omitempty"`
	Driver             string   `json:"driver,omitempty"`
	LinkType           string   `json:"linkType,omitempty"`
}
type Capacity struct {
	Value       string   `json:"value"`
	Default     string   `json:"default"`
	ValidValues []string `json:"validValues"`
}
type Compatibility struct {
	Scope  string   `json:"scope"`
	Domain string   `json:"domain"`
	Groups []string `json:"groups"`
}

// +kubebuilder:validation:Enum=mac;trust;spoofCheck;linkState;vlanId;vlanQoS;vlanProtocol;minTxRate;maxTxRate
type VFSettingField string

// +kubebuilder:validation:Enum=flr
type ResetMethod string

// +kubebuilder:validation:Enum=roce-single-port-v1;ib-verbs-single-port-v1;ib-ipoib-single-port-v1
type RDMAProfile string

type Exposure struct {
	Persona                  string              `json:"persona"`
	Plugin                   string              `json:"plugin"`
	Mode                     string              `json:"mode"`
	AllowMultipleAllocations bool                `json:"allowMultipleAllocations,omitempty"`
	Capacity                 map[string]Capacity `json:"capacity,omitempty"`
	// +kubebuilder:validation:MaxItems=2
	Compatibility     []Compatibility `json:"compatibility,omitempty"`
	EffectPermissions []string        `json:"effectPermissions"`
	AllowedDrivers    []string        `json:"allowedDrivers,omitempty"`
	// +kubebuilder:validation:MaxItems=9
	// +listType=set
	AllowedVFSettings []VFSettingField `json:"allowedVFSettings,omitempty"`
	// +kubebuilder:validation:MaxItems=1
	// +listType=set
	AllowedResetMethods []ResetMethod `json:"allowedResetMethods,omitempty"`
	// +kubebuilder:validation:MaxItems=1
	// +listType=set
	AllowedRDMAProfiles []RDMAProfile `json:"allowedRDMAProfiles,omitempty"`
	AllowedMADAccess    bool          `json:"allowedMADAccess,omitempty"`
	// Approved existing Linux bridges. The administrator alias is an identity
	// fence; a bridge name alone is not authority to attach a representor.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=name
	AllowedHostBridges []HostBridgeGrant `json:"allowedHostBridges,omitempty"`
	// Approved existing OVS bridges. UUID and administrator external ID fence
	// replacement and foreign dataplanes; only explicitly granted VLANs apply.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=name
	AllowedOVSBridges []OVSBridgeGrant `json:"allowedOVSBridges,omitempty"`
}

type OVSBridgeGrant struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]{1,15}$`
	Name string `json:"name"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	UUID string `json:"uuid"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Identity string `json:"identity"`
	// +kubebuilder:validation:MaxItems=4094
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=4094
	// +listType=set
	VLANs []int `json:"vlans"`
}

type HostBridgeGrant struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]{1,15}$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Alias string `json:"alias"`
	// +kubebuilder:validation:MaxItems=4094
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=4094
	// +listType=set
	VLANs []int `json:"vlans"`
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=name
	Uplinks []HostUplinkGrant `json:"uplinks,omitempty"`
}

type HostUplinkGrant struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]{1,15}$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Alias string `json:"alias"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`
	PCIAddress string `json:"pciAddress"`
	// +kubebuilder:validation:MaxItems=4094
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=4094
	// +listType=set
	VLANs []int `json:"vlans"`
}
type PolicySpec struct {
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
	Priority     int32                 `json:"priority,omitempty"`
	Selector     DeviceSelector        `json:"selector"`
	// +kubebuilder:validation:Enum=Expose;Exclude;ManageEswitch
	Action     string             `json:"action"`
	Exposure   *Exposure          `json:"exposure,omitempty"`
	Management *EswitchManagement `json:"management,omitempty"`
}

// EswitchManagement is an administrator's persistent desired mode. It is not
// a workload allocation or a grant to change a mode while consumers exist.
type EswitchManagement struct {
	// +kubebuilder:validation:Enum=ethernet-pf-vfs-v1
	Profile string `json:"profile"`
	// +kubebuilder:validation:Enum=legacy;switchdev
	Mode string `json:"mode"`
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:items:Enum=legacy;switchdev
	// +listType=set
	AllowedModes []string `json:"allowedModes"`
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:Enum=linux/eswitch-configure/v1
	// +listType=set
	EffectPermissions []string `json:"effectPermissions"`
}

type EswitchNodeStatus struct {
	Node               string `json:"node"`
	ObservedGeneration int64  `json:"observedGeneration"`
	PCI                string `json:"pci"`
	Phase              string `json:"phase"`
	Mode               string `json:"mode,omitempty"`
	Message            string `json:"message,omitempty"`
}
type PolicyStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	// +listType=map
	// +listMapKey=node
	EswitchNodes []EswitchNodeStatus `json:"eswitchNodes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=dep
// +kubebuilder:subresource:status
type DeviceExposurePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolicySpec   `json:"spec"`
	Status            PolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DeviceExposurePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceExposurePolicy `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="attachment identity is immutable"
type AttachmentSpec struct {
	PodUID      string      `json:"podUID"`
	SandboxID   string      `json:"sandboxID"`
	NodeName    string      `json:"nodeName"`
	NodeUID     string      `json:"nodeUID"`
	BootID      string      `json:"bootID"`
	ClaimUID    string      `json:"claimUID"`
	Fingerprint string      `json:"fingerprint"`
	RevisionRef RevisionRef `json:"revisionRef"`
}
type Output struct {
	Step       string `json:"step"`
	Port       string `json:"port"`
	Type       string `json:"type"`
	IfName     string `json:"ifName,omitempty"`
	PCIAddress string `json:"pciAddress,omitempty"`
	ShareID    string `json:"shareID,omitempty"`
	RDMADevice string `json:"rdmaDevice,omitempty"`
}
type AttachmentStatus struct {
	Phase     string `json:"phase,omitempty"`
	LastError string `json:"lastError,omitempty"`
	// +listType=map
	// +listMapKey=step
	// +listMapKey=port
	Outputs []Output `json:"outputs,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=nas
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.nodeName`
type NetworkAttachmentStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AttachmentSpec   `json:"spec"`
	Status            AttachmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NetworkAttachmentStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkAttachmentStatus `json:"items"`
}

type TopologyRootConfiguration struct {
	metav1.TypeMeta      `json:",inline"`
	RevisionRef          RevisionRef `json:"revisionRef"`
	RootStep             string      `json:"rootStep"`
	PluginContractDigest string      `json:"pluginContractDigest"`
}
