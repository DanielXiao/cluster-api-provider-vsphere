/*
Copyright 2024 The Kubernetes Authors.

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
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/util/validation/field"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vmwarev1 "sigs.k8s.io/cluster-api-provider-vsphere/api/supervisor/v1beta2"
	"sigs.k8s.io/cluster-api-provider-vsphere/feature"
	"sigs.k8s.io/cluster-api-provider-vsphere/internal/webhooks"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/manager"
	pkgnetwork "sigs.k8s.io/cluster-api-provider-vsphere/pkg/services/network"
)

// +kubebuilder:webhook:verbs=create;update,path=/validate-vmware-infrastructure-cluster-x-k8s-io-v1beta2-vspheremachine,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,groups=vmware.infrastructure.cluster.x-k8s.io,resources=vspheremachines,versions=v1beta2,name=validation.vspheremachine.vmware.infrastructure.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

// VSphereMachine implements a validation and defaulting webhook for VSphereMachine.
type VSphereMachine struct {
	// Client is used to look up the owning VSphereCluster when the
	// PerClusterNetworkProvider feature gate is enabled.
	Client client.Client
	// NetworkProvider is the network provider used by Supervisor based clusters.
	// Used directly when the gate is off, and as a fallback when the gate is on
	// but the owning Cluster has no spec.network.provider set.
	NetworkProvider string
}

var _ admission.Validator[*vmwarev1.VSphereMachine] = &VSphereMachine{}

func (webhook *VSphereMachine) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &vmwarev1.VSphereMachine{}).
		WithValidator(webhook).
		Complete()
}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (webhook *VSphereMachine) ValidateCreate(ctx context.Context, objTyped *vmwarev1.VSphereMachine) (admission.Warnings, error) {
	provider, err := webhook.resolveProvider(ctx, objTyped)
	if err != nil {
		return nil, err
	}
	allErrs := validateNetwork(provider, objTyped.Spec.Network, field.NewPath("spec", "network"))
	return nil, webhooks.AggregateObjErrors(objTyped.GroupVersionKind().GroupKind(), objTyped.Name, allErrs)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (webhook *VSphereMachine) ValidateUpdate(ctx context.Context, oldTyped, newTyped *vmwarev1.VSphereMachine) (admission.Warnings, error) {
	var allErrs field.ErrorList

	newSpec, oldSpec := newTyped.Spec, oldTyped.Spec

	// In VM operator, following fields are immutable, so CAPV should not allow to update them.
	// - ImageName
	// - ClassName
	// - StorageClass
	// - MinHardwareVersion
	if newSpec.ImageName != oldSpec.ImageName {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "imageName"), "cannot be modified"))
	}

	if newSpec.ClassName != oldSpec.ClassName {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "className"), "cannot be modified"))
	}

	if newSpec.StorageClass != oldSpec.StorageClass {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "storageClass"), "cannot be modified"))
	}

	if newSpec.MinHardwareVersion != oldSpec.MinHardwareVersion {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "minHardwareVersion"), "cannot be modified"))
	}

	if !reflect.DeepEqual(newSpec.Network.Interfaces, oldSpec.Network.Interfaces) {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "network", "interfaces"), "cannot be modified"))
	}

	provider, err := webhook.resolveProvider(ctx, newTyped)
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, validateNetwork(provider, newSpec.Network, field.NewPath("spec", "network"))...)

	return nil, webhooks.AggregateObjErrors(newTyped.GroupVersionKind().GroupKind(), newTyped.Name, allErrs)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (webhook *VSphereMachine) ValidateDelete(_ context.Context, _ *vmwarev1.VSphereMachine) (admission.Warnings, error) {
	return nil, nil
}

// resolveProvider returns the network provider string that should be used to
// validate the given object. When the PerClusterNetworkProvider feature gate
// is off the function returns the flag-derived NetworkProvider verbatim.
// When the gate is on it looks up the owning VSphereCluster via the
// cluster.x-k8s.io/cluster-name label and returns its spec.network.provider,
// falling back to the flag-derived NetworkProvider when the field is empty.
// Errors loading the cluster are surfaced so the apiserver can retry the
// admission.
func (webhook *VSphereMachine) resolveProvider(ctx context.Context, obj client.Object) (string, error) {
	if !feature.Gates.Enabled(feature.PerClusterNetworkProvider) {
		return webhook.NetworkProvider, nil
	}
	return resolveNetworkProvider(ctx, webhook.Client, obj.GetNamespace(), obj.GetLabels(), webhook.NetworkProvider)
}

// resolveNetworkProvider returns the spec.network.provider of the owning
// VSphereCluster (if found and non-empty), or the supplied fallback otherwise.
// The owning cluster is looked up via the cluster.x-k8s.io/cluster-name label.
func resolveNetworkProvider(ctx context.Context, c client.Client, namespace string, labels map[string]string, fallback string) (string, error) {
	if c == nil {
		return fallback, nil
	}
	clusterName := labels[clusterv1.ClusterNameLabel]
	if clusterName == "" {
		return fallback, nil
	}

	clusterList := &vmwarev1.VSphereClusterList{}
	if err := c.List(ctx, clusterList, client.InNamespace(namespace), client.MatchingLabels{clusterv1.ClusterNameLabel: clusterName}); err != nil {
		return "", err
	}
	if len(clusterList.Items) == 0 {
		return fallback, nil
	}

	provider := clusterList.Items[0].Spec.Network.Provider
	if provider == "" {
		return fallback, nil
	}
	return provider, nil
}

func validateNetwork(networkProvider string, network vmwarev1.VSphereMachineNetworkSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if !network.Interfaces.IsDefined() {
		return allErrs
	}

	if !feature.Gates.Enabled(feature.MultiNetworks) {
		allErrs = append(allErrs, field.Forbidden(
			fldPath.Child("interfaces"),
			"interfaces can only be set when feature gate MultiNetworks is enabled"))
		return allErrs
	}

	switch networkProvider {
	case manager.NSXVPCNetworkProvider:
		primary := network.Interfaces.Primary
		if primary.IsDefined() {
			primaryNetGVK := primary.NetworkRef.GroupVersionKind()
			if primaryNetGVK != pkgnetwork.NetworkGVKNSXTVPCSubnetSet {
				allErrs = append(allErrs, field.Invalid(
					fldPath.Child("interfaces", "primary", "network"),
					primaryNetGVK,
					fmt.Sprintf("only supports %s", pkgnetwork.NetworkGVKNSXTVPCSubnetSet)))
			}
		}
		for i, secondaryInterface := range network.Interfaces.Secondary {
			secondaryNetGVK := secondaryInterface.NetworkRef.GroupVersionKind()
			if secondaryNetGVK != pkgnetwork.NetworkGVKNSXTVPCSubnetSet && secondaryNetGVK != pkgnetwork.NetworkGVKNSXTVPCSubnet {
				allErrs = append(allErrs, field.Invalid(
					fldPath.Child("interfaces", "secondary").Index(i).Child("network"),
					secondaryNetGVK,
					fmt.Sprintf("only supports %s or %s", pkgnetwork.NetworkGVKNSXTVPCSubnetSet, pkgnetwork.NetworkGVKNSXTVPCSubnet)))
			}
		}
	case manager.VDSNetworkProvider:
		if network.Interfaces.Primary.IsDefined() {
			allErrs = append(allErrs, field.Forbidden(
				fldPath.Child("interfaces", "primary"),
				"primary interface can not be set when network provider is vsphere-network"))
		}
		for i, secondaryInterface := range network.Interfaces.Secondary {
			secondaryNetGVK := secondaryInterface.NetworkRef.GroupVersionKind()
			if secondaryNetGVK != pkgnetwork.NetworkGVKNetOperator {
				allErrs = append(allErrs, field.Invalid(
					fldPath.Child("interfaces", "secondary").Index(i).Child("network"),
					secondaryNetGVK,
					fmt.Sprintf("only supports %s", pkgnetwork.NetworkGVKNetOperator)))
			}
		}
	case manager.HeterogeneousNetworkProvider:
		// For the heterogeneous provider, VSphereMachine.spec.network.interfaces is
		// the source of truth. Skip the per-provider switch entirely; only the
		// structural checks below (interface-name uniqueness) apply.
	default:
		allErrs = append(allErrs, field.Forbidden(fldPath.Child("interfaces"), fmt.Sprintf("interfaces can not be set when network provider is %s", networkProvider)))
	}

	// Validate interface names are unique
	interfaceNames := map[string]struct{}{pkgnetwork.PrimaryInterfaceName: {}}
	for i, secondaryInterface := range network.Interfaces.Secondary {
		if _, ok := interfaceNames[secondaryInterface.Name]; ok {
			allErrs = append(allErrs, field.Invalid(
				fldPath.Child("interfaces", "secondary").Index(i).Child("name"),
				secondaryInterface.Name,
				"interface name is already in use"))
		} else {
			interfaceNames[secondaryInterface.Name] = struct{}{}
		}
	}
	return allErrs
}
