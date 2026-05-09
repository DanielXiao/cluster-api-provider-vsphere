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

package vmware

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"

	infrav1 "sigs.k8s.io/cluster-api-provider-vsphere/api/govmomi/v1beta2"
	vmwarev1 "sigs.k8s.io/cluster-api-provider-vsphere/api/supervisor/v1beta2"
	capvcontext "sigs.k8s.io/cluster-api-provider-vsphere/pkg/context"
)

// VMModifier allows a function to be passed to VM creation to modify its spec
// The hook is loosely typed so as to allow for different VirtualMachine backends.
type VMModifier func(runtime.Object) (runtime.Object, error)

// NetworkProviderCapabilities exposes the subset of services.NetworkProvider
// behavior that downstream packages (e.g. vmoperator) need without creating
// an import cycle through the services package (which depends on this package
// for the ClusterContext type).
type NetworkProviderCapabilities interface {
	// SupportsVMReadinessProbe indicates whether this provider supports a VM
	// readiness probe on the control-plane VM.
	SupportsVMReadinessProbe() bool
}

// SupervisorMachineContext is a Go capvcontext used with a VSphereMachine.
type SupervisorMachineContext struct {
	*capvcontext.BaseMachineContext
	VSphereCluster *vmwarev1.VSphereCluster
	VSphereMachine *vmwarev1.VSphereMachine
	VMModifiers    []VMModifier

	// NetworkProvider is the NetworkProvider resolved for this Cluster on this
	// reconcile. It is expected to be non-nil for supervisor-based reconciles.
	// Stored on the context (rather than threaded through every method
	// signature) so the existing VSphereMachineService interface, shared with
	// the govmomi path, can stay unchanged.
	NetworkProvider NetworkProviderCapabilities
}

// String returns VSphereMachineGroupVersionKind VSphereMachineNamespace/VSphereMachineName.
func (c *SupervisorMachineContext) String() string {
	return fmt.Sprintf("%s %s/%s", c.VSphereMachine.GroupVersionKind(), c.VSphereMachine.Namespace, c.VSphereMachine.Name)
}

// Patch updates the object and its status on the API server.
func (c *SupervisorMachineContext) Patch(ctx context.Context) error {
	return c.PatchHelper.Patch(ctx, c.VSphereMachine,
		patch.WithOwnedV1Beta1Conditions{Conditions: []clusterv1.ConditionType{
			clusterv1.ReadyV1Beta1Condition,
			infrav1.VMProvisionedV1Beta1Condition,
		}},
		patch.WithOwnedConditions{Conditions: []string{
			infrav1.VSphereMachineReadyCondition,
			infrav1.VSphereMachineVirtualMachineProvisionedCondition,
			clusterv1.PausedCondition,
		}})
}

// GetVSphereMachine returns the VSphereMachine from the SupervisorMachineContext.
func (c *SupervisorMachineContext) GetVSphereMachine() capvcontext.VSphereMachine {
	return c.VSphereMachine
}

// GetReady return when the VSphereMachine is ready.
func (c *SupervisorMachineContext) GetReady() bool {
	return ptr.Deref(c.VSphereMachine.Status.Initialization.Provisioned, false)
}

// GetObjectMeta returns the metadata for the VSphereMachine from the SupervisorMachineContext.
func (c *SupervisorMachineContext) GetObjectMeta() metav1.ObjectMeta {
	return c.VSphereMachine.ObjectMeta
}

// GetClusterContext returns the Cluster and VSphereCluster from the SupervisorMachineContext.
func (c *SupervisorMachineContext) GetClusterContext() *ClusterContext {
	return &ClusterContext{
		Cluster:        c.Cluster,
		VSphereCluster: c.VSphereCluster,
	}
}

// SetBaseMachineContext sets the BaseMachineContext for the SupervisorMachineContext.
func (c *SupervisorMachineContext) SetBaseMachineContext(base *capvcontext.BaseMachineContext) {
	c.BaseMachineContext = base
}
