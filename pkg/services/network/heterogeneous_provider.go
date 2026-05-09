/*
Copyright 2026 The Kubernetes Authors.

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

package network

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/util/conditions"

	vmwarev1 "sigs.k8s.io/cluster-api-provider-vsphere/api/supervisor/v1beta2"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/context/vmware"
	vmoprvhub "sigs.k8s.io/cluster-api-provider-vsphere/pkg/conversion/api/vmoperator/hub"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/services"
)

// heterogeneousNetworkProvider is the network provider used by Supervisor Clusters
// that host workload Clusters spanning more than one network type. The Supervisor
// Cluster's load balancer and control-plane endpoint are pre-created by the
// consumer that creates the Supervisor, so CAPV does not reconcile a
// VirtualMachineService and does not provision per-cluster networks.
//
// VSphereMachine.spec.network.interfaces is the source of truth for the per-VM
// network configuration; it is propagated verbatim to vm.spec.network.interfaces
// on every reconcile.
type heterogeneousNetworkProvider struct{}

// HeterogeneousNetworkProvider returns an instance of the heterogeneous network provider.
func HeterogeneousNetworkProvider() services.NetworkProvider {
	return &heterogeneousNetworkProvider{}
}

func (np *heterogeneousNetworkProvider) HasLoadBalancer() bool { return false }

func (np *heterogeneousNetworkProvider) SupportsVMReadinessProbe() bool { return false }

func (np *heterogeneousNetworkProvider) ProvisionClusterNetwork(_ context.Context, clusterCtx *vmware.ClusterContext) error {
	conditions.Set(clusterCtx.VSphereCluster, metav1.Condition{
		Type:   vmwarev1.VSphereClusterNetworkReadyCondition,
		Status: metav1.ConditionTrue,
		Reason: vmwarev1.VSphereClusterNetworkReadyReason,
	})
	return nil
}

func (np *heterogeneousNetworkProvider) GetClusterNetworkName(_ context.Context, _ *vmware.ClusterContext) (string, error) {
	return "", nil
}

func (np *heterogeneousNetworkProvider) GetVMServiceAnnotations(_ context.Context, _ *vmware.ClusterContext) (map[string]string, error) {
	return map[string]string{}, nil
}

func (np *heterogeneousNetworkProvider) VerifyNetworkStatus(_ context.Context, _ *vmware.ClusterContext, _ runtime.Object) error {
	return nil
}

// ConfigureVirtualMachine treats VSphereMachine.spec.network.interfaces as the
// source of truth and translates it into vm.spec.network.interfaces verbatim.
// This matches the re-assert-on-every-reconcile pattern used by the per-provider
// implementations.
func (np *heterogeneousNetworkProvider) ConfigureVirtualMachine(_ context.Context, _ *vmware.ClusterContext, machine *vmwarev1.VSphereMachine, vm *vmoprvhub.VirtualMachine) error {
	vm.Spec.Network = &vmoprvhub.VirtualMachineNetworkSpec{}

	if machine.Spec.Network.Interfaces.Primary.IsDefined() {
		primary := machine.Spec.Network.Interfaces.Primary
		var mtu *int64
		if primary.MTU != 0 {
			mtu = ptr.To(int64(primary.MTU))
		}
		vmInterface := vmoprvhub.VirtualMachineNetworkInterfaceSpec{
			Name: PrimaryInterfaceName,
			Network: &vmoprvhub.PartialObjectRef{
				TypeMeta: metav1.TypeMeta{
					Kind:       primary.NetworkRef.Kind,
					APIVersion: primary.NetworkRef.APIVersion,
				},
				Name: primary.NetworkRef.Name,
			},
			MTU: mtu,
		}
		setRoutes(&vmInterface, primary.Routes)
		vm.Spec.Network.Interfaces = append(vm.Spec.Network.Interfaces, vmInterface)
	}

	setVMSecondaryInterfaces(machine, vm)
	return nil
}
