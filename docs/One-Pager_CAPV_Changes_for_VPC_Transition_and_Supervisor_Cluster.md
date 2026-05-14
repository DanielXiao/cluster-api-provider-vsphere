# One-Pager: CAPV Changes for VPC Transition and Supervisor Cluster

## Business Problem Being Solved

Today CAPV picks its network provider once, from the `--network-provider`
command-line flag, and holds a single `services.NetworkProvider` instance for
the entire process. This is incompatible with two upcoming VKS features that a
single CAPV process must serve at the same time:

- [VKS Cluster Transition from VDS / NSX T1 to NSX VPC](./One-Pager_Support_VKS_Cluster_Transition_to_VPC.md) --
  the network provider is per-namespace, and pre-existing Clusters keep their
  original provider after their namespace transitions to VPC. CAPV must apply
  per-provider defaulting, validation, and reconciliation rules on a
  per-Cluster basis.
- [Advanced `networks.interfaces.options` for the Supervisor Cluster](./One-Pager_Advanced_Networks_Interfaces_Options_for_Supervisor_Cluster.md) --
  the Supervisor Cluster picks a concrete workload provider per-cluster but
  uses a small set of `options` to selectively turn off provider-driven
  behavior (cluster-network provisioning, control-plane TCP probe,
  provider-based webhook validation, provider-driven VM rewriting).

This document consolidates the CAPV-side changes required by both designs.
Everything outside CAPV (the per-Cluster network provider label, the GCC
Cluster mutating / validating webhook, the CAPI Runtime Extension, VM Operator)
is described in the upstream documents and is out of scope here.

## Goals

- Resolve the network provider per `VSphereCluster` from
  `VSphereCluster.spec.network.provider`, on every controller reconcile and
  every webhook admission call.
- Honor cluster-scoped options on `VSphereCluster.spec.network`
  (`skipProvisionClusterNetwork`, `skipControlPlaneTCPProbe`) and
  machine-scoped options on
  `VSphereMachine.spec.network.interfaces.options`
  (`skipProviderBasedValidation`, `straightPropagateToVMSpec`,
  `primaryInterfaceIPAMModes`).
- Tolerate Clusters whose `spec.network.provider` is not yet populated. The
  CAPI Runtime Extension may patch the `VSphereMachineTemplate` (driving
  `VSphereMachine` creation) before it patches the `VSphereClusterTemplate`,
  so controllers and webhooks must not fail permanently in this window.
- Keep the `--network-provider` flag path bit-for-bit compatible when the
  feature gate `NetworkProviderFromVSphereCluster` is off.

## Non-Goals

- Authoring the per-Cluster network provider label, the GCC Cluster
  webhooks, or the CAPI Runtime Extension.
- Changing any concrete provider's internal logic
  (`netop_provider.go`, `nsxt_provider.go`, `nsxt_vpc_provider.go`).
- Changing the `services.NetworkProvider` interface signatures.
- Introducing a new "heterogeneous" network provider value. Heterogeneity is
  expressed entirely via `networks.interfaces.options`; the provider value
  on `VSphereCluster.spec.network.provider` is always one of
  `vsphere-network`, `NSX`, `NSX-VPC`.

## Big Picture

`VSphereCluster.spec.network.provider` is the per-cluster knob, written by the
CAPI Runtime Extension from the per-Cluster network provider label
(`clusters.kubernetes.vmware.com/network-provider`). CAPV resolves it on every
controller reconcile and every webhook admission call.

```yaml
apiVersion: vmware.infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereCluster
spec:
  network:
    provider: vsphere-network | NSX | NSX-VPC
    skipProvisionClusterNetwork: true   # advanced option, Supervisor Cluster only
    skipControlPlaneTCPProbe: true      # advanced option, Supervisor Cluster only
```

A new feature gate `NetworkProviderFromVSphereCluster` is the master switch. Off:
the `--network-provider` flag drives everything (today's behavior). On:
`VSphereCluster.spec.network.provider` is the source of truth.

## Lazy Construction of Network Provider

CAPV does not pick its provider at startup. Instead, a
`NetworkProviderFactory` resolves the right `NetworkProvider` for a given
`VSphereCluster` on every controller reconcile.

The factory holds a small registry of pre-built provider singletons (one per
kind: `vsphere-network`, `NSX`, `NSX-VPC`). `ForCluster` reads
`VSphereCluster.spec.network.provider` and returns the matching singleton.

If `VSphereCluster.spec.network.provider` is empty, CAPV treats the Cluster as
"not yet ready for network reconciliation":

- **Controllers** raise an error like `"Network Provider is empty, wait for a
  valid value"` and requeue. They do not fall back to the flag, do not pick a
  default provider, and do not write anything network-related into downstream
  resources. The reconcile loop simply waits for the Runtime Extension to
  populate the field.
- **Webhooks** skip provider-based validation on `VSphereCluster`,
  `VSphereMachine`, and `VSphereMachineTemplate`. Schema-level checks
  (interface name uniqueness, structural required fields, `mtu` max value,
  etc.) still run. This is required because the Runtime Extension may patch
  the `VSphereMachineTemplate` before it patches the `VSphereClusterTemplate`;
  rejecting the machine template in that window would block the Cluster
  rollout.

When the gate is off, the `--network-provider` flag is the active provider for
all clusters; the field is ignored.

```mermaid
sequenceDiagram
    autonumber
    participant Main as setupSupervisorControllers
    participant Fac as NetworkProviderFactory
    participant Reg as registry<br/>map[provider]NetworkProvider
    participant Ctl as VSphereCluster /<br/>VSphereMachine Reconciler
    participant WH as VSphereCluster / VSphereMachine /<br/>VSphereMachineTemplate Webhook
    participant NP as services.NetworkProvider

    rect rgb(245,245,245)
    note over Main,Reg: Manager startup (once)
    alt NetworkProviderFromVSphereCluster gate ON
        Main->>Fac: NewPerClusterNetworkProviderFactory(client)
        Fac->>Reg: pre-build<br/>{vsphere-network, NSX, NSX-VPC}
    else gate OFF
        Main->>Fac: NewStaticNetworkProviderFactory(client, --network-provider)
        Fac->>Reg: pre-build single provider<br/>from --network-provider
    end
    Main->>Ctl: inject factory into supervisor controllers
    end

    rect rgb(245,250,245)
    note over Ctl,NP: Controller reconcile (lazy resolution)
    Ctl->>Fac: ForCluster(ctx, vsphereCluster)

    alt gate OFF
        Fac-->>Ctl: flag-based provider, nil
        Ctl->>NP: ProvisionClusterNetwork / ConfigureVirtualMachine /<br/>HasLoadBalancer / SupportsVMReadinessProbe / ...
    else gate ON, Spec.Network.Provider set
        Fac->>Reg: lookup by Spec.Network.Provider
        Reg-->>Fac: provider instance
        Fac-->>Ctl: np, nil
        Ctl->>NP: ProvisionClusterNetwork / ConfigureVirtualMachine /<br/>HasLoadBalancer / SupportsVMReadinessProbe / ...
    else gate ON, Spec.Network.Provider == ""
        Fac-->>Ctl: nil, ErrNetworkProviderEmpty
        Ctl-->>Ctl: log "Network Provider is empty,<br/>wait for a valid value"<br/>and requeue
    end
    end

    rect rgb(250,245,245)
    note over WH: Webhook admission (lazy validation)
    WH->>WH: load owning VSphereCluster
    alt gate OFF
        WH->>WH: apply per-provider validation<br/>using --network-provider flag
    else gate ON, Spec.Network.Provider set
        WH->>WH: apply per-provider validation rules
    else gate ON, Spec.Network.Provider == ""
        WH->>WH: skip provider-based validation;<br/>run only schema-level checks
    end
    end
```

### `NetworkProviderFactory`

The factory is consumed by controllers only. Webhooks read
`VSphereCluster.spec.network.provider` directly to decide which
per-provider validation rules to apply; they do not need a `NetworkProvider`
instance.

```go
// NetworkProviderFactory resolves the services.NetworkProvider for a given
// VSphereCluster on demand. One instance per CAPV process; safe for
// concurrent use.
type NetworkProviderFactory interface {
    // ForCluster returns the NetworkProvider for the given VSphereCluster.
    // Returns ErrNetworkProviderEmpty when the per-cluster factory is in
    // use and Spec.Network.Provider is unset; the caller should requeue
    // and wait for the Runtime Extension to populate the field.
    ForCluster(ctx context.Context, cluster *vmwarev1.VSphereCluster) (services.NetworkProvider, error)
}

// ErrNetworkProviderEmpty is returned by ForCluster when
// VSphereCluster.spec.network.provider is empty under the per-cluster
// factory.
var ErrNetworkProviderEmpty = errors.New("Network Provider is empty, wait for a valid value")
```

#### `perClusterNetworkProviderFactory` (gate ON)

Pre-builds one provider per kind and dispatches on
`VSphereCluster.spec.network.provider`.

```go
// NewPerClusterNetworkProviderFactory returns a factory that dispatches
// on VSphereCluster.spec.network.provider.
func NewPerClusterNetworkProviderFactory(c client.Client) NetworkProviderFactory {
    return &perClusterNetworkProviderFactory{
        registry: map[string]services.NetworkProvider{
            VDSNetworkProvider:    network.NetOpNetworkProvider(c),
            NSXNetworkProvider:    network.NsxtNetworkProvider(c, "false"),
            NSXVPCNetworkProvider: network.NSXTVpcNetworkProvider(c),
        },
    }
}

type perClusterNetworkProviderFactory struct {
    registry map[string]services.NetworkProvider
}

func (f *perClusterNetworkProviderFactory) ForCluster(
    _ context.Context, cluster *vmwarev1.VSphereCluster,
) (services.NetworkProvider, error) {
    name := cluster.Spec.Network.Provider
    if name == "" {
        return nil, ErrNetworkProviderEmpty
    }
    np, ok := f.registry[name]
    if !ok {
        return nil, fmt.Errorf("unknown network provider %q", name)
    }
    return np, nil
}
```

#### `staticNetworkProviderFactory` (gate OFF)

Pre-builds a single provider from the `--network-provider` flag and always
returns it, regardless of the `VSphereCluster` value -- behaviorally
identical to today.

```go
// NewStaticNetworkProviderFactory returns a factory that always returns a
// single provider built from the --network-provider flag value. Used when
// the NetworkProviderFromVSphereCluster feature gate is off.
func NewStaticNetworkProviderFactory(c client.Client, flagValue string) (NetworkProviderFactory, error) {
    np, err := GetNetworkProvider(context.Background(), c, flagValue)
    if err != nil {
        return nil, fmt.Errorf("building static network provider %q: %w", flagValue, err)
    }
    return &staticNetworkProviderFactory{np: np}, nil
}

type staticNetworkProviderFactory struct {
    np services.NetworkProvider
}

func (f *staticNetworkProviderFactory) ForCluster(
    _ context.Context, _ *vmwarev1.VSphereCluster,
) (services.NetworkProvider, error) {
    return f.np, nil
}
```

The factory is constructed once at startup on the supervisor path and threaded
into the supervisor cluster / machine controllers as an explicit parameter
(not stored on `ControllerManagerContext`, to avoid an import cycle and to
keep it swappable in tests). The govmomi path passes `nil`; the supervisor
controllers reject `nil`.

## API Changes

### Feature Gate

| Gate | Default | Purpose |
|---|---|---|
| `NetworkProviderFromVSphereCluster` (new) | `false` (alpha) | Off: `--network-provider` flag drives everything via `staticNetworkProviderFactory`. On: `VSphereCluster.spec.network.provider` is the source of truth via `perClusterNetworkProviderFactory`, and `networks.interfaces.options` are honored. |

### `VSphereCluster.spec.network`

Add an optional `provider` field, plus two cluster-scoped option fields
populated by the CAPI Runtime Extension from
`networks.interfaces.options`:

```yaml
apiVersion: vmware.infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereCluster
spec:
  network:
    provider: vsphere-network | NSX | NSX-VPC
    skipProvisionClusterNetwork: true
    skipControlPlaneTCPProbe: true
```

- `provider` is validated by a CRD enum on the three known values plus the
  empty string.
- `provider` is **immutable once set to a non-empty value.** Transitioning
  a brownfield namespace re-labels the Cluster but never flips its CAPV
  provider value (per the VKS transition design). The empty -> non-empty
  write is allowed, so the Runtime Extension can populate the field after
  the Cluster is created.
- `skipProvisionClusterNetwork` and `skipControlPlaneTCPProbe` default to
  `false`. They are mutable -- the Runtime Extension propagates them on
  every reconcile from the Cluster topology variable.
- A printer column on `VSphereCluster` surfaces `provider`.

### `VSphereMachine.spec.network.interfaces.options`

Add a new `options` block under `VSphereMachine.spec.network.interfaces`,
populated by the Runtime Extension and applied per-machine:

```yaml
apiVersion: vmware.infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereMachine
spec:
  network:
    interfaces:
      options:
        skipProviderBasedValidation: true
        straightPropagateToVMSpec: true
        primaryInterfaceIPAMModes:
        - IPv4
        - IPv6
      primary:
        network:
          apiVersion: crd.nsx.vmware.com/v1alpha1
          kind: Subnet
          name: supervisor-workload
      secondary:
      - name: eth1
        network:
          apiVersion: netoperator.vmware.com/v1alpha1
          kind: Network
          name: management-network
```

The same `options` block exists on `VSphereMachineTemplate.spec.template.spec.network.interfaces`.
All three boolean / list options default to off / empty, preserving today's
behavior. They are intended for the Supervisor Cluster only and are gated
upstream by a namespace annotation enforced by the GCC Cluster webhook.

## Changes to Validation Webhooks

The CAPV webhooks for `VSphereCluster`, `VSphereMachine`, and
`VSphereMachineTemplate` decide which per-provider validation rules to apply.

### Resolution Order (gate ON)

On every admission call:

1. Load the owning `VSphereCluster` (via the `cluster.x-k8s.io/cluster-name`
   label for `VSphereMachine` / `VSphereMachineTemplate`; itself for
   `VSphereCluster`).
2. If the request is for `VSphereMachine` or `VSphereMachineTemplate` and
   `spec.network.interfaces.options.skipProviderBasedValidation == true`,
   **skip provider-based validation** and run only schema-level checks
   (interface name uniqueness, `mtu` max value, structural required fields).
3. Else if `cluster.Spec.Network.Provider` is non-empty, apply the
   per-provider validation rules below.
4. Else (`cluster.Spec.Network.Provider` is empty), **skip provider-based
   validation** and run only schema-level checks. This covers the window in
   which the Runtime Extension has patched the `VSphereMachineTemplate` but
   not yet the `VSphereClusterTemplate`.
5. If the owning cluster cannot be loaded (transient API error), surface the
   error so the apiserver retries the admission.

### Per-Provider Rules (unchanged)

Applied only when the provider is resolved and
`skipProviderBasedValidation` is not set.

| Provider | `VSphereMachine.spec.network.interfaces` rules |
|---|---|
| `vsphere-network` (VDS) | `primary` forbidden; `secondary` must be `netoperator.vmware.com/Network`. |
| `NSX` (T1) | `interfaces` not supported. |
| `NSX-VPC` | `primary` must be VPC `SubnetSet`; `secondary` may be VPC `SubnetSet` or `Subnet`. |

### `VSphereCluster` Update Admission

- Rejects mutation of `spec.network.provider` once it is set to a non-empty
  value.
- `skipProvisionClusterNetwork` and `skipControlPlaneTCPProbe` are mutable
  (propagated by the Runtime Extension on every reconcile).

### Gate OFF

Behavior is unchanged. The flag-derived `webhook.NetworkProvider` string is
the active provider for all clusters; `spec.network.provider` and
`networks.interfaces.options` are ignored.

## Changes to Controllers and Reconciled Resources

This section describes how the reconciled resources (`VirtualMachineService`,
the cluster-scoped network object, `VirtualMachine`) behave under each
combination of provider and option, and which controller code changes
deliver that behavior.

### Controller Wiring

#### `controllers/vspherecluster_controller.go` (setup)

`AddClusterControllerToManager` gains a
`networkProviderFactory services.NetworkProviderFactory` parameter, required
when `supervisorBased`. The factory is built once in
`setupSupervisorControllers` (per the gate, `NewPerClusterNetworkProviderFactory`
or `NewStaticNetworkProviderFactory`) and forwarded into
`vmware.ClusterReconciler`. The existing
`vmware.ClusterReconciler.NetworkProvider` field is replaced by
`NetworkProviderFactory`.

#### `controllers/vmware/vspherecluster_reconciler.go`

At the top of `reconcileNormal`, resolve once. If
`ErrNetworkProviderEmpty` is returned, log
`"Network Provider is empty, wait for a valid value"` and requeue without
error. Otherwise replace all `r.NetworkProvider.*` uses with `np.*`.

#### `controllers/vspheremachine_controller.go`

`AddMachineControllerToManager` gains the same
`networkProviderFactory` parameter. The reconciler:

- Drops `r.networkProvider`; stores the factory instead.
- Resolves `np` per reconcile after the cluster context is available. On
  empty provider, requeues -- it has no business configuring VM networks
  until the Cluster's provider is known.
- Threads `np` into `r.VMService` and into
  `np.ConfigureVirtualMachine(...)`.

#### `pkg/services/vmoperator/vmopmachine.go`

Drop the constructor-time `ConfigureControlPlaneVMReadinessProbe bool`. Make
the per-reconcile decision from `np` plus the cluster-scoped option:

```go
if !clusterCtx.VSphereCluster.Spec.Network.SkipControlPlaneTCPProbe &&
    np.SupportsVMReadinessProbe() &&
    infrautilv1.IsControlPlaneMachine(machine) {
    // configure TCP-based readiness probe
}
```

`np` is threaded in as a parameter on the `VMService` reconcile method (not
stored on the context struct, to keep behavioral knobs out of state).

#### `pkg/services/vmoperator/control_plane_endpoint.go`

No code change. Already accepts `netProvider services.NetworkProvider`; the
caller now passes the lazily-resolved `np`.

### `VirtualMachineService` (control-plane LB)

Reconciled by `control_plane_endpoint.go`, which short-circuits when
`!netProvider.HasLoadBalancer()`. Behavior is unchanged: every concrete
provider returns `HasLoadBalancer() = true`, so a `VirtualMachineService` is
reconciled.

The Supervisor Cluster suppresses the LB by pre-setting
`Cluster.spec.controlPlaneEndpoint` (which already short-circuits LB
creation in CAPV today) -- not via a `NetworkProvider` toggle. No code
change here for either upstream design.

### Cluster Network

Driven by `np.ProvisionClusterNetwork` / `np.GetClusterNetworkName`,
gated by the new cluster-scoped option `skipProvisionClusterNetwork`.

```go
if !clusterCtx.VSphereCluster.Spec.Network.SkipProvisionClusterNetwork {
    if err := np.ProvisionClusterNetwork(ctx, clusterCtx); err != nil {
        return err
    }
}
```

| Provider | `skipProvisionClusterNetwork` | Effect |
|---|---|---|
| `vsphere-network` | `false` | Resolve the namespace's default `Network` (existing). |
| `NSX` | `false` | Auto-create a `VirtualNetwork` (existing). |
| `NSX-VPC` | `false` | Reconcile the per-cluster `SubnetSet` (existing). |
| any | `true` | **No-op.** The networks referenced by `interfaces` already exist; CAPV does not provision them. `NetworkReady` is set to `True` immediately. `GetClusterNetworkName`, `GetVMServiceAnnotations`, `VerifyNetworkStatus` are also skipped. |

Today this is the Supervisor Cluster's path: its workload network is a
pre-created `Subnet` referenced by `networks.interfaces`, so CAPV must not
try to provision a per-cluster network on top of it.

### `VirtualMachine` (per-machine network configuration)

Driven by `np.ConfigureVirtualMachine(ctx, clusterCtx, vsphereMachine, vm)`,
gated by the new machine-scoped option `straightPropagateToVMSpec`. The
controller also propagates `primaryInterfaceIPAMModes` onto the resulting
primary interface.

```go
opts := vsphereMachine.Spec.Network.Interfaces.Options

if opts.StraightPropagateToVMSpec {
    vm.Spec.Network.Interfaces = deepCopyInterfaces(vsphereMachine.Spec.Network.Interfaces)
} else {
    if err := np.ConfigureVirtualMachine(ctx, clusterCtx, vsphereMachine, vm); err != nil {
        return err
    }
}

if len(opts.PrimaryInterfaceIPAMModes) > 0 {
    setPrimaryIPAMModes(vm, opts.PrimaryInterfaceIPAMModes)
}
```

| Provider | `straightPropagateToVMSpec` | Effect |
|---|---|---|
| `vsphere-network` / `NSX` / `NSX-VPC` | `false` | Existing per-provider logic. |
| any | `true` | **`VSphereMachine.spec.network.interfaces` is the source of truth.** Deep-copy `interfaces` (primary + secondaries, including `routes`, `mtu`, `gateway4/6`, etc.) verbatim into `vm.Spec.Network.Interfaces` on every reconcile, matching the re-assert pattern every other provider already uses. No provider-driven rewriting. |

`primaryInterfaceIPAMModes`, when non-empty, is written verbatim to the
primary (`eth0`) entry's `ipamModes` in
`VirtualMachine.spec.network.interfaces`. When empty / unset, `ipamModes` is
left unset and the VM Operator network provider's default applies.

### Control-Plane VM Readiness Probe

Driven by `np.SupportsVMReadinessProbe()`, gated by the new cluster-scoped
option `skipControlPlaneTCPProbe`.

| Provider | `skipControlPlaneTCPProbe` | Probe configured? |
|---|---|---|
| `vsphere-network` / `NSX` / `NSX-VPC` | `false` | Per `np.SupportsVMReadinessProbe()` (existing). |
| any | `true` | **No.** Used by the Supervisor Cluster, whose control-plane VIP is pre-created and not fronted by a CAPV-managed `VirtualMachineService` to probe. |

## Backward Compatibility

- **`--network-provider` flag.** Continues to work unchanged. Gate off, it
  drives everything via `staticNetworkProviderFactory`. Gate on, it is
  ignored at runtime; per-cluster resolution is driven entirely by
  `VSphereCluster.spec.network.provider`.
- **Pre-existing Clusters under gate on.** They start with an empty
  `spec.network.provider`. Controllers requeue with
  `"Network Provider is empty, wait for a valid value"` and webhooks skip
  provider-based validation until the CAPI Runtime Extension populates the
  field, after which normal per-provider behavior resumes.
- **`networks.interfaces.options` under gate off.** The new
  `VSphereCluster.spec.network` and
  `VSphereMachine.spec.network.interfaces.options` fields are ignored at
  runtime. Operators wishing to enable Supervisor-Cluster-style heterogeneous
  networking must turn the gate on.
- **Mixed deployments.** If the upstream label is never written (e.g. the
  Supervisor capability is off), the Runtime Extension never writes
  `spec.network.provider`, and CAPV must run with the gate off (operator
  policy, not enforced inside CAPV).
