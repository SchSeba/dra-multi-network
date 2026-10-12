/*
 * Copyright 2025 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package consts

import (
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
)

const (
	DriverName = "dra-multi-network.schseba.github.io"

	AttributePciAddress    = DriverName + "/pciAddress"
	AttributePFName        = DriverName + "/PFName"
	AttributeEswitchMode   = DriverName + "/EswitchMode"
	AttributeVendorID      = DriverName + "/vendor"
	AttributeDeviceID      = DriverName + "/deviceID"
	AttributePFDeviceID    = DriverName + "/pfDeviceID"
	AttributeVFID          = DriverName + "/vfID"
	AttributeInterfaceType = DriverName + "/interfaceType"
	AttributeLinkType      = DriverName + "/linkType"
	AttributeRDMACapable   = DriverName + "/rdmaCapable"
	// Use upstream Kubernetes standard attribute prefix for pciAddress
	AttributeStandardPciAddress = deviceattribute.StandardDeviceAttributePrefix + "pciBusID"
	// AttributePfPciAddress is for the PCI address of the Physical Function (PF).
	AttributePfPciAddress = DriverName + "/pfPciAddress"

	// this is the most-common nonstandard prefix, supported by dranet and dracpu
	DraNetCompatPrefix = "dra.net"
	AttributeNUMANode  = DraNetCompatPrefix + "/numaNode"

	// Network device constants
	NetClass  = 0x02 // Network controller class
	SysBusPci = "/sys/bus/pci/devices"

	// Eswitch mode constants (as reported by devlink)
	EswitchModeLegacy    = "legacy"
	EswitchModeSwitchdev = "switchdev"

	// Link type constants
	LinkTypeEth        = "eth"
	LinkTypeEthernet   = "ethernet"
	LinkTypeIB         = "ib"
	LinkTypeInfiniband = "infiniband"
	LinkTypeUnknown    = "unknown"

	// Interface type constants
	// VirtualFunction is a virtual function of a physical function present on the host
	InterfaceTypeVirtualFunction = "VirtualFunction"
	// Regular is a regular network interface present on the host with valid pci address
	InterfaceTypeRegular = "Regular"

	// RDMA device constants
	SysClassInfiniband = "/sys/class/infiniband"
)

// Kubernetes standard attributes
var (
	// AttributePCIeRoot identifies the PCIe root complex of the device
	AttributePCIeRoot resourceapi.QualifiedName = deviceattribute.StandardDeviceAttributePCIeRoot
)
