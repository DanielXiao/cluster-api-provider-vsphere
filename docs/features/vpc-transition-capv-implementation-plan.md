# CAPV Implementation Plan: VKS Cluster Transition (VDS / NSX T1 → NSX VPC)

This plan covers **only** the CAPV changes required by
[One-Pager: Support VKS Cluster Transition from VDS / NSX T1 to NSX VPC Network](../one-pager-vks-cluster-transition.md),
i.e. the per-cluster network provider feature gated by `ClusterNetworkProvider`.

The two other features described in
[One-Pager: CAPV Changes for VPC Transition, Supervisor Cluster and IPv6/Dual-Stack](../one-pager-capv-changes-vpc-transition.md)
— the **externally-managed** network provider and **IPv6/Dual-Stack** (`IPv6DualStack` gate) — are
explicitly **out of scope** here and will be delivered in their own PRs.

## Scope summary

In scope for this PR:
1. New feature gate `ClusterNetworkProvider` (alpha, default off).
2. New API field `VSphereCluster.spec.network.provider` (enum + immutability), in **both** `v1beta1` and `v1beta2` (direct field-to-field conversion).
3. A `NetworkProviderFactory` abstraction with a per-cluster factory (gate on) and a static factory (gate off).
4. Lazy, per-cluster resolution of the `NetworkProvider` in the supervisor `VSphereCluster` and `VSphereMachine` controllers.
5. Per-cluster provider resolution in the `VSphereCluster`, `VSphereMachine`, and `VSphereMachineTemplate` webhooks, plus immutability enforcement on `VSphereCluster` update.
6. Rename the `pkg/manager` network provider constants to the new names (`vsphere-distributed | nsx-tier1 | vpc`). The VKS Carvel package performs the legacy↔new conversion upstream and always passes `--network-provider <new name>`, so CAPV does not need a mapping helper.

Explicitly **out of scope** (deferred to the other two PRs):
- The `externally-managed` provider value and its `network.ExternallyManagedNetworkProvider`.
- The new `SkipCreateSupervisorService()` interface method and the `ServiceDiscovery` controller change.
- All `IPv6DualStack` behavior (`SubnetSet.ipAddressType`/`accessMode`, `DeriveIPAMModes`, dual-stack `VirtualMachineService`, KCP `certSANs` readiness gate, NSX Operator API bump).
- DHCP `SubnetSet`-as-primary handling — this lives in the GCC webhook / Runtime Extension, not CAPV (assumption; see [Open questions](#open-questions)).
- Any change to the internal logic of the existing providers (`netop_provider.go`, `nsxt_provider.go`, `nsxt_vpc_provider.go`) or to the existing `services.NetworkProvider` method signatures.

## Decisions assumed (questions were skipped)

- **Enum values**: this PR adds only `vsphere-distributed | nsx-tier1 | vpc`. The `externally-managed` value is added by its own PR (adding an enum value is a backward-compatible CRD change).
- **Naming**: rename the existing `pkg/manager` constants from the legacy names (`NSX-VPC`, `NSX`, `vsphere-network`) to the new names (`vpc`, `nsx-tier1`, `vsphere-distributed`). No mapping helper — the VKS Carvel package converts legacy→new and passes `--network-provider <new name>`.
- **Versioning**: add `provider` to **both** the `v1beta1` spoke and the `v1beta2` hub. Because the field exists identically on both versions, the generated `Convert_*` functions copy it directly and `v1beta1 ↔ v1beta2` round-trips preserve it without any `restored`-annotation logic.
- **Gate OFF**: webhooks and controllers behave exactly as today, driven by the `--network-provider` flag via the static factory.

---

## 1. Feature gate `ClusterNetworkProvider`

**File:** `feature/feature.go`
- Add a new `featuregate.Feature` constant `ClusterNetworkProvider` (mirroring the existing constants such as `MultiNetworks`, lines 32–35).
- Register it in the `supervisorGates` map (lines 83–87) with `{Default: false, PreRelease: featuregate.Alpha}`.

**File:** `feature/gates_test.go`
- Extend the gate registration assertions to include the new gate.

**Notes:** This is a plain (non-versioned) supervisor gate; it does not need an entry in `supervisorVersionedGates`.

---

## 2. API: `VSphereCluster.spec.network.provider`

Add the **same** `Provider` field to the `Network` struct in **both** API versions, with identical JSON tag, enum, and CEL marker so the generated conversion copies it directly.

**File:** `api/supervisor/v1beta2/vspherecluster_types.go`
- Add a `Provider` string field to the existing `Network` struct (currently lines 162–169, which holds only `NSXVPC`).
  - JSON tag `provider`, `+optional`.
  - `+kubebuilder:validation:Enum=vsphere-distributed;nsx-tier1;vpc` (the three in-scope values).
  - Add a CEL immutability rule allowing empty→non-empty but forbidding mutation of a non-empty value, e.g. a `+kubebuilder:validation:XValidation` on the `Network` struct in the style of the existing `nsxVPC` rule (line 163). The rule must permit the empty→non-empty transition (Runtime Extension populates it post-create / post-upgrade) and reject any change once set.
- The existing `Network` already drops `+kubebuilder:validation:MinProperties=1`-style guards; confirm `provider` participates correctly with the existing `MinProperties` constraint.
- Update `Network.IsDefined()` (lines 171–174) if needed so a `Network` carrying only `provider` is considered defined (the current `reflect.DeepEqual` against the zero `Network{}` already handles this, but re-verify after the field is added).

**File:** `api/supervisor/v1beta1/vspherecluster_types.go`
- Add the **identical** `Provider` field to the `v1beta1` `Network` struct (lines 160–167), with the same JSON tag `provider`, `+optional`, `+kubebuilder:validation:Enum=vsphere-distributed;nsx-tier1;vpc`, and the matching CEL immutability `XValidation` marker.
- Re-verify `v1beta1` `Network.IsDefined()` (lines 169–172) the same way as the hub.

**File:** `api/supervisor/v1beta2/zz_generated.deepcopy.go` and `api/supervisor/v1beta1/zz_generated.deepcopy.go`
- Regenerate (no manual edit) — `provider` is a value string so deepcopy is trivial; regeneration keeps both consistent.

**Conversion (now a direct copy, no `restored` logic):**
- **File:** `api/supervisor/v1beta1/zz_generated.conversion.go` — regenerate via `make generate`. Because both `Network` structs now carry `Provider`, the generated `Convert_v1beta1_Network_To_v1beta2_Network` / `Convert_v1beta2_Network_To_v1beta1_Network` copy it field-to-field automatically.
- **File:** `api/supervisor/v1beta1/conversion.go` — **no change needed** for `provider`; do **not** add a `restored`-annotation branch for it (the existing `restored` pattern at lines 42–49 stays only for the unrelated `Status.Initialization` handling).
- **File:** `api/supervisor/v1beta1/conversion_test.go` — the fuzz round-trip test (`FuzzTestFunc`) automatically exercises the new field; ensure it passes (it should with no extra logic, since the field exists on both sides).

**CRD manifests:**
- Regenerate the supervisor CRDs (`make generate` / `make generate-manifests`) so `config/supervisor/crd/...` picks up the new enum + CEL rule. Do not hand-edit generated CRDs.

---

## 3. Provider names

The new `spec.network.provider` field uses the names `vsphere-distributed | nsx-tier1 | vpc`. The VKS
Carvel package performs the legacy↔new conversion (`NSX→nsx-tier1`, `NSX-VPC→vpc`,
`vsphere-network→vsphere-distributed`) and always passes `--network-provider <new name>`, so CAPV
only needs to standardize on the new names — no mapping helper is required.

**File:** `pkg/manager/network.go`
- Change the constant **values** (lines 31–35) to the new names: `NSXVPCNetworkProvider = "vpc"`, `NSXNetworkProvider = "nsx-tier1"`, `VDSNetworkProvider = "vsphere-distributed"`. Keep the constant *identifiers* the same so existing call sites compile unchanged.
- `GetNetworkProvider` (lines 42–63) keeps switching on these constants; because the flag now carries the new names, the existing `switch` matches directly. Leave `DummyLBNetworkProvider` / default behavior unchanged.

**Call sites that benefit automatically (no edit needed beyond the rename):**
- The webhook `validateNetwork` switch (`internal/webhooks/vmware/vspheremachine.go`, lines 112–150) keyed on `manager.NSXVPCNetworkProvider` / `manager.VDSNetworkProvider` now compares against the new names, which equal the `spec.network.provider` values directly.
- The `nsxVPC`-only-when-`vpc` checks in `vspherecluster.go` (line 75) and `vsphereclustertemplate.go` (line 74).

**Caution:** search the repo for any **hardcoded** legacy strings (`"NSX-VPC"`, `"NSX"`, `"vsphere-network"`) outside these constants — e.g. tests, e2e config, sample manifests, or deployment YAML that sets `--network-provider` — and update them to the new names. The flag now expects the new provider names; coordinate the rollout with the Carvel package change.

---

## 4. `NetworkProviderFactory`

**New file:** `pkg/manager/network_factory.go` (same package as `GetNetworkProvider`; alternatively `pkg/services/network/factory.go` if we prefer to keep it next to the providers — pick one and keep it consistent).

Define (interfaces/types only; no behavior beyond what the one-pager specifies):
- `NetworkProviderFactory` interface with `ForCluster(ctx, *vmwarev1.VSphereCluster) (services.NetworkProvider, error)`.
- `ErrNetworkProviderEmpty` sentinel error (`"network provider is empty, wait for a valid value"`).
- `perClusterNetworkProviderFactory`: holds a registry `map[string]services.NetworkProvider` pre-built for the **three** in-scope keys (the renamed `vsphere-distributed` / `nsx-tier1` / `vpc` constants) via `GetNetworkProvider`. `ForCluster` reads `cluster.Spec.Network.Provider`; returns `ErrNetworkProviderEmpty` when empty, the matching singleton when known, and a `"unknown network provider %q"` error otherwise. (No `externally-managed` entry in this PR.)
- `staticNetworkProviderFactory`: wraps a single provider built from the `--network-provider` flag (`GetNetworkProvider`) and always returns it.
- Constructors `NewPerClusterNetworkProviderFactory(client)` and `NewStaticNetworkProviderFactory(client, flagValue)`.

**New file:** `pkg/manager/network_factory_test.go` — unit tests for both factories (empty → error, known → singleton, unknown → error; static always returns the flag provider).

---

## 5. Controller wiring

### 5.1 Manager startup

**File:** `main.go`
- In `setupSupervisorControllers` (line 651): build a single `NetworkProviderFactory` based on `feature.Gates.Enabled(feature.ClusterNetworkProvider)` — per-cluster factory when on, static factory (from `controllerCtx.NetworkProvider`) when off.
- Pass the factory (and the manager `client`) into `controllers.AddClusterControllerToManager` and `controllers.AddMachineControllerToManager`, and into the webhook `SetupWebhookWithManager` calls (lines 652–663). The flag registration at line 183 is unchanged.

> Implementation choice: either change the `AddClusterControllerToManager` / `AddMachineControllerToManager` signatures to accept the factory, or carry the factory on `capvcontext.ControllerManagerContext`. Prefer passing it explicitly to keep the gate decision in `main.go`.

### 5.2 `VSphereCluster` controller

**File:** `controllers/vspherecluster_controller.go` — `AddClusterControllerToManager` (lines 61–98)
- Replace the single `networkProvider` built at line 65 with the injected `NetworkProviderFactory`; set it on the reconciler (replace the `NetworkProvider:` field assignment at line 78).

**File:** `controllers/vmware/vspherecluster_reconciler.go`
- `ClusterReconciler` struct (line 64): replace `NetworkProvider services.NetworkProvider` with `NetworkProviderFactory <factory type>`.
- In `reconcileNormal` (around line 255–294, before `ProvisionClusterNetwork` at line 279): call `factory.ForCluster(ctx, clusterCtx.VSphereCluster)` once to resolve `np`.
  - On `ErrNetworkProviderEmpty`: log `"network provider is empty, wait for a valid value"`, set `VSphereClusterNetworkReadyCondition=False` with an appropriate reason, and requeue (return a `ctrl.Result{}` with requeue / a retryable error). Do **not** provision anything.
  - On other errors: return the error.
- Use the resolved `np` at every existing `r.NetworkProvider.*` call site: `ProvisionClusterNetwork` (line 279), `HasLoadBalancer` (lines 314, 332, 338), and `ReconcileControlPlaneEndpointService` (line 361). Thread `np` through `reconcileControlPlaneEndpoint` / `reconcileLoadBalancedEndpoint` as a parameter instead of reading a struct field.

### 5.3 `VSphereMachine` controller

**File:** `controllers/vspheremachine_controller.go`
- `AddMachineControllerToManager` (lines 88–140): replace the eager `networkProvider` (lines 98–103) with the injected factory stored on the reconciler. Note the current setup also sets `VmopMachineService.ConfigureControlPlaneVMReadinessProbe` once from `networkProvider.SupportsVMReadinessProbe()` (line 103) — this must become per-cluster (see below).
- `machineReconciler` struct (lines 182–188): replace `networkProvider services.NetworkProvider` with the factory.
- In `Reconcile` (resolve after the owning `Cluster`/`VSphereCluster` are known, around lines 239–250): fetch the `VSphereCluster` (via `cluster.Spec.InfrastructureRef`) and call `factory.ForCluster` to get `np`.
  - Empty provider → log + requeue (no VM modifiers, no provisioning), consistent with the cluster controller.
- `setVMModifiers` (lines 509–528): use the resolved `np.ConfigureVirtualMachine` (line 520) instead of `r.networkProvider`.
- **Readiness probe per-cluster:** `pkg/services/vmoperator/vmopmachine.go` consumes `ConfigureControlPlaneVMReadinessProbe bool` (field at line 60, used at line 623). Since the provider is now per-cluster, set this per reconcile from `np.SupportsVMReadinessProbe()` — e.g. construct/seed the `VmopMachineService` (or pass the bool through the machine context/`ReconcileNormal`) at reconcile time rather than once at manager startup. Keep the change minimal and localized to the supervisor path.

> The control-plane endpoint short-circuit on a pre-set `Cluster.spec.controlPlaneEndpoint` (in `control_plane_endpoint.go`) is unchanged.

---

## 6. Webhooks (per-cluster provider resolution)

All three webhooks currently receive a static `NetworkProvider string` and switch on the legacy
constants. Under the gate they must instead resolve the provider per-cluster.

### 6.1 Wiring

**File:** `webhooks/vmware/alias.go`
- Extend the `SetupWebhookWithManager` signatures (lines 29–55) so each webhook also receives the manager `client` (needed to load the owning `VSphereCluster`). Keep passing the flag-derived `NetworkProvider` string for the gate-off path.

**File:** `main.go` — update the four webhook setup calls (lines 652–663) accordingly (pass `mgr.GetClient()`).

### 6.2 Shared resolution helper

Add a helper (e.g. in `internal/webhooks/vmware/`) that, when `ClusterNetworkProvider` is enabled, returns the provider name to validate against, following the one-pager resolution order:
1. For `VSphereMachine` / `VSphereMachineTemplate`: read the `cluster.x-k8s.io/cluster-name` label, load the `Cluster`, then load the `VSphereCluster` via `Cluster.spec.infrastructureRef`.
2. If the `VSphereCluster` cannot be loaded → reject (surface the error).
3. If `VSphereCluster.spec.network.provider` is empty → reject (surface the error).
4. Else return the provider value as-is; it equals the renamed `pkg/manager` constant the existing `validateNetwork` switch expects, so no translation is needed.

When the gate is **off**, the helper returns the static flag value (today's behavior) and never loads a cluster.

> RBAC: the webhook now reads `clusters` and `vsphereclusters`. The controllers already hold these `get;list;watch` permissions (see kubebuilder markers in `controllers/vspherecluster_controller.go` lines 51–54 and the cluster watch), so manager-level RBAC should already cover it — verify after regeneration.

### 6.3 `VSphereMachine` webhook

**File:** `internal/webhooks/vmware/vspheremachine.go`
- `ValidateCreate`/`ValidateUpdate` (lines 53, 61) call `validateNetwork(webhook.NetworkProvider, ...)` (lines 54, 91). Replace the static `webhook.NetworkProvider` with the resolved provider from the helper (gate-aware). The internal `switch` in `validateNetwork` (lines 102–150) stays as-is, keyed on the renamed constants which now match the field values.
- Empty/unloadable owning cluster → reject (per resolution order).

### 6.4 `VSphereMachineTemplate` webhook

**File:** `internal/webhooks/vmware/vspheremachinetemplate.go`
- `validate` (line 108) calls `validateNetwork(webhook.NetworkProvider, ...)` (line 109). Resolve the provider the same way (load owning cluster via the template's `cluster-name` label). If a template has no resolvable owning cluster under the gate, reject per the resolution order.

### 6.5 `VSphereCluster` webhook

**File:** `internal/webhooks/vmware/vspherecluster.go`
- `validateClusterNetwork` (lines 66–83): the `nsxVPC`-only-when-`NSX-VPC` check (line 75) should use the cluster's own `spec.network.provider` (mapped) when the gate is on, instead of the static `webhook.NetworkProvider`.
- **Immutability** of a non-empty `spec.network.provider` (allow empty→non-empty, reject any later change) is enforced **solely by the CRD CEL rule** on the `Network` struct (§2). The webhook does **not** duplicate this check — we keep a single source of truth for the immutability contract to avoid drift.
- Add enum/known-value validation for `provider` on create as a secondary guard (the CRD enum is primary).

### 6.6 `VSphereClusterTemplate` webhook

**File:** `internal/webhooks/vmware/vsphereclustertemplate.go`
- A `VSphereClusterTemplate` does not belong to any `Cluster`, so there is no `spec.network.provider` to resolve. When the `ClusterNetworkProvider` gate is **on**, **skip the provider-based check** in `validateClusterTemplateNetwork` (the `nsxVPC`-only-when-`vpc` block at lines 74–79); keep only the structural / feature-gate validation (the `MultiNetworks` check at lines 68–73). When the gate is **off**, behavior is unchanged (flag-based).

---

## 7. Tests

Update/extend:
- `controllers/vmware/vspherecluster_reconciler_test.go` (line 75 sets `NetworkProvider`) → switch to a factory; add cases for empty-provider requeue and each provider.
- `controllers/vmware/test/controllers_test.go` (lines 250, 344 set `NetworkProvider` / use `DummyLBNetworkProvider`) → factory-based wiring.
- `controllers/vspheremachine_controller.go` machine tests + `pkg/services/vmoperator/vmopmachine_test.go` (lines 201, 1292) for the per-cluster readiness-probe wiring.
- Webhook tests: `internal/webhooks/vmware/vspherecluster_test.go`, `vspheremachine_test.go`, `vspheremachinetemplate_test.go`, `vsphereclustertemplate_test.go` — add gate-on cases (empty provider rejects, per-provider rules resolved from the owning `VSphereCluster`, immutability on update) and confirm gate-off behavior is unchanged.
- `feature/gates_test.go` — new gate registered.
- `api/supervisor/v1beta1/conversion_test.go` — round-trip preserves `provider` (now via direct field copy, no `restored` logic).
- New `pkg/manager/network_factory_test.go`.

---

## 8. Backward compatibility

- **`--network-provider` flag**: unchanged. Gate off → static factory drives everything (bit-for-bit). Gate on → flag ignored at runtime; per-cluster resolution drives behavior.
- **Pre-existing clusters under gate on**: start with empty `spec.network.provider`; controllers requeue with the "wait for a valid value" message and webhooks reject mutating admission until the Runtime Extension populates the field (empty→non-empty allowed), after which normal per-provider behavior resumes.
- **No change** to existing provider implementations, the `services.NetworkProvider` interface, or the `ServiceDiscovery`/control-plane-endpoint logic.

---

## 9. File-change checklist

| Area | File(s) | Change |
| --- | --- | --- |
| Feature gate | `feature/feature.go`, `feature/gates_test.go` | Add + register `ClusterNetworkProvider` (alpha, off) |
| API | `api/supervisor/v1beta2/vspherecluster_types.go`, `api/supervisor/v1beta1/vspherecluster_types.go` | Add `Network.Provider` (enum + immutability CEL) to **both** versions |
| API (gen) | `api/supervisor/v1beta2/zz_generated.deepcopy.go`, `api/supervisor/v1beta1/zz_generated.deepcopy.go`, CRDs under `config/supervisor/...` | Regenerate |
| Conversion | `api/supervisor/v1beta1/zz_generated.conversion.go`, `conversion_test.go` | Regenerate (direct field copy; no manual `restored` logic) |
| Names | `pkg/manager/network.go` | Rename constant values to new names (`vpc` / `nsx-tier1` / `vsphere-distributed`) |
| Factory | `pkg/manager/network_factory.go` (+ `_test.go`) | `NetworkProviderFactory`, per-cluster + static impls, `ErrNetworkProviderEmpty` |
| Startup | `main.go` | Build factory by gate; inject into controllers + webhooks |
| Cluster ctrl | `controllers/vspherecluster_controller.go`, `controllers/vmware/vspherecluster_reconciler.go` | Inject factory; `ForCluster` per reconcile; empty→requeue |
| Machine ctrl | `controllers/vspheremachine_controller.go` | Inject factory; resolve `np` per reconcile; per-cluster readiness probe |
| Machine svc | `pkg/services/vmoperator/vmopmachine.go` | Make readiness-probe config per-cluster |
| Webhooks | `webhooks/vmware/alias.go`, `internal/webhooks/vmware/vspherecluster.go`, `vspheremachine.go`, `vspheremachinetemplate.go` | Gate-aware per-cluster resolution (provider immutability is enforced by the CRD CEL rule, not the webhook) |
| Tests | files listed in §7 | Update/extend |
