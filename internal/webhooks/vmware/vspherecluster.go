/*
Copyright 2025 The Kubernetes Authors.

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

// Package vmware is the package for webhooks of vmware resources.
package vmware

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vmwarev1 "sigs.k8s.io/cluster-api-provider-vsphere/api/supervisor/v1beta2"
	"sigs.k8s.io/cluster-api-provider-vsphere/feature"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/manager"
)

// +kubebuilder:webhook:verbs=create;update,path=/validate-vmware-infrastructure-cluster-x-k8s-io-v1beta2-vspherecluster,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,groups=vmware.infrastructure.cluster.x-k8s.io,resources=vsphereclusters,versions=v1beta2,name=validation.vspherecluster.vmware.infrastructure.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

// VSphereCluster implements a validation and defaulting webhook for VSphereCluster.
type VSphereCluster struct {
	// NetworkProvider is the network provider used by Supervisor based clusters.
	// When the PerClusterNetworkProvider feature gate is on, this is consulted
	// only as the fallback for Clusters whose spec.network.provider is empty
	// (i.e. pre-existing Clusters that pre-date the per-cluster label rollout).
	NetworkProvider string
}

var _ admission.Validator[*vmwarev1.VSphereCluster] = &VSphereCluster{}

func (webhook *VSphereCluster) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &vmwarev1.VSphereCluster{}).
		WithValidator(webhook).
		Complete()
}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (webhook *VSphereCluster) ValidateCreate(_ context.Context, obj *vmwarev1.VSphereCluster) (admission.Warnings, error) {
	return webhook.validateClusterNetwork(nil, obj)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (webhook *VSphereCluster) ValidateUpdate(_ context.Context, oldTyped, newTyped *vmwarev1.VSphereCluster) (admission.Warnings, error) {
	return webhook.validateClusterNetwork(oldTyped, newTyped)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (webhook *VSphereCluster) ValidateDelete(_ context.Context, _ *vmwarev1.VSphereCluster) (admission.Warnings, error) {
	return nil, nil
}

func (webhook *VSphereCluster) validateClusterNetwork(oldCluster, cluster *vmwarev1.VSphereCluster) (admission.Warnings, error) {
	var allErrs field.ErrorList
	if !feature.Gates.Enabled(feature.MultiNetworks) && cluster.Spec.Network.NSXVPC.CreateSubnetSet != nil {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "network", "nsxVPC", "createSubnetSet"), "createSubnetSet can only be set when MultiNetworks feature gate is enabled"))
	}

	// Resolve the active network provider for this cluster.
	// Gate-on:  prefer cluster.spec.network.provider, fall back to the
	//           --network-provider flag for empty values (pre-existing Clusters).
	// Gate-off: always use the --network-provider flag value.
	activeProvider := webhook.NetworkProvider
	if feature.Gates.Enabled(feature.PerClusterNetworkProvider) && cluster.Spec.Network.Provider != "" {
		activeProvider = cluster.Spec.Network.Provider
	}

	if cluster.Spec.Network.NSXVPC.IsDefined() && activeProvider != manager.NSXVPCNetworkProvider {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "network", "nsxVPC"), "nsxVPC can only be set when network provider is NSX-VPC"))
	}

	// Reject mutation of spec.network.provider once it is set. Update calls
	// (oldCluster != nil) compare old vs new; once set, the field is immutable.
	if oldCluster != nil && oldCluster.Spec.Network.Provider != "" && oldCluster.Spec.Network.Provider != cluster.Spec.Network.Provider {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "network", "provider"), "field is immutable once set"))
	}

	if len(allErrs) > 0 {
		return nil, apierrors.NewInvalid(cluster.GroupVersionKind().GroupKind(), cluster.Name, allErrs)
	}
	return nil, nil
}
