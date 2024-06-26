/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package network contains code for configuring network services.
package network

const (
	// NSXTTypeNetwork is the name of the NSX-T network type.
	NSXTTypeNetwork = "nsx-t"
	// NSXTVNetSelectorKey is also defined in VM Operator.
	NSXTVNetSelectorKey = "ncp.vmware.com/virtual-network-name"
	// NSXTVPCSubnetSetNetworkType is the name of the NSX VPC network type. Please refer to:
	// https://github.com/vmware-tanzu/vm-operator/blob/main/api/v1alpha1/virtualmachine_types.go#L149.
	NSXTVPCSubnetSetNetworkType = "nsx-t-subnetset"

	// CAPVDefaultNetworkLabel is a label used to identify the default network.
	CAPVDefaultNetworkLabel = "capv.vmware.com/is-default-network"
	// NetOpNetworkNameAnnotation is the key used in an annotation to define the NetOp network. The expected value is the network name.
	NetOpNetworkNameAnnotation = "netoperator.vmware.com/network-name"

	// SystemNamespace is the namespace where supervisor control plane VMs reside.
	SystemNamespace = "kube-system"

	// legacyDefaultNetworkLabel was the label used for default networks.
	//
	// Deprecated: legacyDefaultNetworkLabel will be removed in a future release.
	legacyDefaultNetworkLabel = "capw.vmware.com/is-default-network"
)
