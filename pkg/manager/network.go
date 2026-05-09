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

package manager

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api-provider-vsphere/feature"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/context/vmware"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/services"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/services/network"
)

const (
	// NSXVPCNetworkProvider identifies the nsx-vpc network provider.
	NSXVPCNetworkProvider = "NSX-VPC"
	// NSXNetworkProvider identifies the NSX network provider.
	NSXNetworkProvider = "NSX"
	// VDSNetworkProvider identifies the VDS network provider.
	VDSNetworkProvider = "vsphere-network"
	// HeterogeneousNetworkProvider identifies the heterogeneous network provider for the
	// Supervisor Cluster. It is only valid as a per-VSphereCluster value when the
	// PerClusterNetworkProvider feature gate is enabled.
	HeterogeneousNetworkProvider = "heterogeneous"
	// DummyLBNetworkProvider identifies the Dummy network provider.
	DummyLBNetworkProvider = "DummyLBNetworkProvider"
)

// GetNetworkProvider will return a network provider instance based on the environment
// the cfg is used to initialize a client that talks directly to api-server without using the cache.
func GetNetworkProvider(ctx context.Context, client client.Client, networkProvider string) (services.NetworkProvider, error) {
	log := ctrl.LoggerFrom(ctx)

	switch networkProvider {
	case NSXVPCNetworkProvider:
		log.Info("Pick NSX-VPC network provider")
		return network.NSXTVpcNetworkProvider(client), nil
	case NSXNetworkProvider:
		// TODO: disableFirewall not configurable
		log.Info("Pick NSX-T network provider")
		return network.NsxtNetworkProvider(client, "false"), nil
	case VDSNetworkProvider:
		log.Info("Pick NetOp (VDS) network provider")
		return network.NetOpNetworkProvider(client), nil
	case HeterogeneousNetworkProvider:
		log.Info("Pick Heterogeneous network provider")
		return network.HeterogeneousNetworkProvider(), nil
	case DummyLBNetworkProvider:
		log.Info("Pick Dummy network provider")
		return network.DummyLBNetworkProvider(), nil
	default:
		log.Info("NetworkProvider not set. Pick Dummy network provider")
		return network.DummyNetworkProvider(), nil
	}
}

// NewNetworkProviderFactory returns a services.NetworkProviderFactory whose concrete type
// depends on the PerClusterNetworkProvider feature gate.
//
// When the gate is off, the factory always returns the same provider instance
// constructed from the --network-provider flag (behaviorally identical to the
// pre-feature-gate code path).
//
// When the gate is on, the factory holds a registry of pre-built provider
// instances (one per known provider value) and dispatches per-reconcile based
// on VSphereCluster.spec.network.provider. The flag value is used as a fallback
// for pre-existing Clusters whose spec.network.provider is empty.
func NewNetworkProviderFactory(ctx context.Context, c client.Client, flagProvider string) (services.NetworkProviderFactory, error) {
	if !feature.Gates.Enabled(feature.PerClusterNetworkProvider) {
		flagBuilt, err := GetNetworkProvider(ctx, c, flagProvider)
		if err != nil {
			return nil, err
		}
		return &staticNetworkProviderFactory{provider: flagBuilt}, nil
	}

	registry := map[string]services.NetworkProvider{
		VDSNetworkProvider:           network.NetOpNetworkProvider(c),
		NSXNetworkProvider:           network.NsxtNetworkProvider(c, "false"),
		NSXVPCNetworkProvider:        network.NSXTVpcNetworkProvider(c),
		HeterogeneousNetworkProvider: network.HeterogeneousNetworkProvider(),
	}

	flagBuilt, err := GetNetworkProvider(ctx, c, flagProvider)
	if err != nil {
		return nil, err
	}

	return &perClusterNetworkProviderFactory{
		registry:     registry,
		flagProvider: flagProvider,
		flagBuilt:    flagBuilt,
	}, nil
}

// staticNetworkProviderFactory is the gate-off implementation; it always returns
// the single provider instance built from the --network-provider flag.
type staticNetworkProviderFactory struct {
	provider services.NetworkProvider
}

func (s *staticNetworkProviderFactory) ForCluster(_ context.Context, _ *vmware.ClusterContext) (services.NetworkProvider, error) {
	return s.provider, nil
}

func (s *staticNetworkProviderFactory) ForProvider(_ context.Context, _ string) (services.NetworkProvider, error) {
	return s.provider, nil
}

// perClusterNetworkProviderFactory is the gate-on implementation; it dispatches
// based on VSphereCluster.spec.network.provider, falling back to the flag-built
// provider when the field is empty.
type perClusterNetworkProviderFactory struct {
	registry     map[string]services.NetworkProvider
	flagProvider string
	flagBuilt    services.NetworkProvider
}

func (p *perClusterNetworkProviderFactory) ForCluster(ctx context.Context, clusterCtx *vmware.ClusterContext) (services.NetworkProvider, error) {
	if clusterCtx == nil || clusterCtx.VSphereCluster == nil {
		return nil, fmt.Errorf("cannot resolve network provider: cluster context is nil")
	}
	return p.ForProvider(ctx, clusterCtx.VSphereCluster.Spec.Network.Provider)
}

func (p *perClusterNetworkProviderFactory) ForProvider(_ context.Context, name string) (services.NetworkProvider, error) {
	if name == "" {
		return p.flagBuilt, nil
	}
	if np, ok := p.registry[name]; ok {
		return np, nil
	}
	return nil, fmt.Errorf("unknown network provider %q", name)
}
