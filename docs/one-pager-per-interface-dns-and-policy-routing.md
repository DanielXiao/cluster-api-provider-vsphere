# One-Pager: Per-Interface DNS and Policy-Based Routing for the Supervisor Cluster

| Author | ETA | JIRA |
| :--- | :--- | :--- |
| Yifeng Xiao | 2 months | GCM-19846, GCM-19842 |

## Business Problem Being Solved

Supervisor Cluster 2.0 is a dual-NIC cluster provisioned by VKS. It needs DNS and Policy-Based Routing (PBR) configured per interface.

**DNS**

* The primary interface `eth0` carries Kubernetes traffic on the workload network. It needs a routing-only search domain (e.g. `~cluster.local`) and the CoreDNS Cluster IP as its name server.
* The secondary interface `eth1` carries management traffic to infrastructure (NSX, ESXi, VC, etc.). It needs the name servers and search domains of the management network.

**Policy-Based Routing**

In Supervisor 1.0, route table `200` routes destination traffic to the workload network (Pod/Service/VM/Ingress). Supervisor 2.0 requires the same table, but VKS cannot create route tables or rules itself. Workload network IP ranges may also change on Day 2 (driven by customers), so we must update routes in place without rolling out new nodes.

## Limitations of the Current In-Place Update

Two limitations block us from using in-place update today:

* **OS network config must come from a single source of truth: cloud-init** (via `guestinfo.metadata` provided by vm-operator). We plumb network settings (name servers, search domains, routes, and PBR) from `VSphereMachine` and `VirtualMachine` CRs. Day 1 boot is driven by cloud-init; the machine-agent then periodically checks `guestinfo.metadata` for Day 2 changes and re-renders the netplan/networkd configuration. However, changing these settings changes the `VSphereMachine` spec, and in-place update only handles `MachineConfig` changes today. The Runtime Extension in-place hook is meant to update the `VirtualMachine` spec, but there is no guidance on how to stop the CAPV `VSphereMachine` controller from also updating it. The Runtime Extension patches `VirtualMachine`, while the machine-agent that performs the actual update only touches `MachineConfig`, so the Runtime Extension cannot verify that the in-place update is complete in order to report status in `UpdateMachineResponse`.
* **Control plane nodes always use a spare node during in-place updates**, and this is not configurable per cluster.

**Plan:** ship DNS and PBR via rolling update first, then switch to in-place update once the limitations above are resolved.

## Goals

* Let the Supervisor configure DNS and Policy-Based Routing per interface in the Supervisor namespace. Since customers may also want to configure them on their Workload clusters, support the same on the NSX VPC network.
* Prepare what is needed to switch to in-place update later.

## Non-Goals

* Support on NSX T1 or VDS networks. Company strategy is to transition these to NSX VPC, so new features should not be added there.

## Architecture Areas

This design builds on [One-Pager: A externally-managed CAPV Network Provider for the Supervisor Cluster](./one-pager-externally-managed-capv-network-provider.md), which introduced the externally-managed CAPV network provider for dual-NIC Supervisor Clusters. This design adds DNS and PBR to the NSX VPC and externally-managed network providers.

### Cluster API Runtime Extension

Expose optional fields `nameservers`, `searchDomains`, `routes.table`, and `routingPolicy` in the Cluster `networks` variable. Example for the Supervisor primary interface:

```yaml
            nameservers:                 # Plumb into VSphereMachine
            - 172.24.0.5                 # CoreDNS Cluster IP
            searchDomains:               # Plumb into VSphereMachine
            - ~cluster.local             # Routing-only search domain
            routes:
            - to: default                # "default" is allowed only when table is set; the default route in the main table is always supplied by vm-operator from the referenced network
              via: 192.168.1.1
              table: 200                 # Plumb into VSphereMachine and VirtualMachine
            - to: 192.168.0.0/16
              table: 200
            routingPolicy:               # Plumb into VSphereMachine and VirtualMachine
            - from: all
              to: 172.16.0.0/16
              table: 200
              priority: 0

```

Propagate these settings into `VSphereMachineTemplate` / `VSphereMachine`.

### Guest Cluster Controller – Cluster Webhook

Add validation:

* If `routes.to` is `default`, `table` must be set. This avoids adding multiple default routes to the main route table, which would break the CNI.
* If the Cluster network provider is VDS or NSX T1, `nameservers`, `searchDomains`, and `routingPolicy` must not be set.

### Cluster API vSphere Provider (CAPV)

#### API Change

Expose the same fields (optional) in `VSphereMachineTemplate` / `VSphereMachine`.

#### Network Provider Implementation Change

In the VPC and externally-managed provider implementations, `ConfigureVirtualMachine(ctx, clusterCtx, machine, vm) error` propagates the interface configuration straight into the `VirtualMachine` spec, including the new fields above.

### Capability Flag

The existing VKS capability flag `supervisor_network_provider` gates the feature end-to-end: Runtime Extension behavior, GCC webhook, and CAPV.

## End-to-End Example

Supervisor Cluster manifest in a namespace annotated as the Supervisor system namespace. Both workload and management networks are VDS `Network` objects:

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: supervisor
  namespace: vmware-system-supervisor
spec:
  controlPlaneEndpoint:
    host: 192.168.0.2
    port: 6443
  clusterNetwork:
    pods:
      cidrBlocks:
      - 192.0.2.0/16
    serviceDomain: cluster.local        # Kubernetes Service Domain
    services:
      cidrBlocks:
      - 198.51.100.0/12
  topology:
    class: builtin-generic-v3.8.0
    version: v1.34.4
    controlPlane:
      replicas: 1
    workers:
      machineDeployments:
      - class: node-pool
        name: workers
        replicas: 1
    variables:
    - name: storageClass
      value: wcpglobal-storage-profile
    - name: vmClass
      value: best-effort-small
    - name: networks
      value:
        interfaces:
          primary:                       # eth0 -> workload (default route)
            network:
              apiVersion: netoperator.vmware.com/v1alpha1
              kind: Network
              name: supervisor-workload
            nameservers:
            - 172.24.0.10                # CoreDNS Cluster IP
            searchDomains:
            - ~cluster.local             # Routing-only search domain
            routes:
            - to: default
              via: 192.168.1.1
              table: 200
            - to: 192.168.0.0/16
              table: 200
            routingPolicy:
            - from: all
              to: 172.16.0.0/16
              table: 200
              priority: 0
            - from: all
              to: 172.24.0.0/16
              table: 200
              priority: 0
          secondary:
          - name: eth1                   # eth1 -> management
            network:
              apiVersion: netoperator.vmware.com/v1alpha1
              kind: Network
              name: management-network
            nameservers:
            - 10.10.0.1                  # management network DNS servers
            - 10.10.0.2
            searchDomains:               # management network search domains
            - dvm.lvn.broadcom.net
            - lvn.broadcom.net
```

The Runtime Extension propagates `networks.interfaces` from the topology variable into `VSphereMachineTemplate`, and the CAPI MachineSet controller propagates it into `VSphereMachine.spec.network.interfaces`:

```yaml
apiVersion: vmware.infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereMachine
metadata:
  name: supervisor-workers-85kxh-7kp5v
  namespace: vmware-system-supervisor
spec:
  network:
    interfaces:
      primary:
        network:
          apiVersion: netoperator.vmware.com/v1alpha1
          kind: Network
          name: supervisor-workload
        nameservers:
        - 172.24.0.10
        searchDomains:
        - ~cluster.local
        routes:
        - to: default
          via: 192.168.1.1
          table: 200
        - to: 192.168.0.0/16
          table: 200
        routingPolicy:
        - from: all
          to: 172.16.0.0/16
          table: 200
          priority: 0
        - from: all
          to: 172.24.0.0/16
          table: 200
          priority: 0
      secondary:
      - name: eth1
        network:
          apiVersion: netoperator.vmware.com/v1alpha1
          kind: Network
          name: management-network
        nameservers:
        - 10.10.0.1
        - 10.10.0.2
        searchDomains:
        - dvm.lvn.broadcom.net
        - lvn.broadcom.net
```

The CAPV `VSphereMachine` controller propagates this straight into the `VirtualMachine` spec:

```yaml
apiVersion: vmoperator.vmware.com/v1alpha6
kind: VirtualMachine
metadata:
  name: supervisor-cp-99tgd
  namespace: vmware-system-supervisor
spec:
  network:
    interfaces:
    - name: eth0
      network:
        apiVersion: netoperator.vmware.com/v1alpha1
        kind: Network
        name: supervisor-workload
      nameservers:
      - 172.24.0.10
      searchDomains:
      - ~cluster.local
      routes:
      - to: default
        via: 192.168.1.1
        table: 200
      - to: 192.168.0.0/16
        table: 200
      routingPolicy:
      - from: all
        to: 172.16.0.0/16
        table: 200
        priority: 0
      - from: all
        to: 172.24.0.0/16
        table: 200
        priority: 0
    - name: eth1
      gateway4: None
      gateway6: None
      network:
        apiVersion: netoperator.vmware.com/v1alpha1
        kind: Network
        name: management-network
      nameservers:
      - 10.10.0.1
      - 10.10.0.2
      searchDomains:
      - dvm.lvn.broadcom.net
      - lvn.broadcom.net
```

Finally, vm-operator renders the cloud-init (`guestinfo.metadata`) spec:

```yaml
network:
  ethernets:
    eth0:
      accept-ra: false
      addresses:
      - 192.168.128.44/16
      dhcp4: false
      dhcp6: false
      gateway4: 192.168.1.1
      match:
        macaddress: 00:50:56:99:b8:08
      nameservers:
        search:
        - ~cluster.local
        addresses:
        - 172.24.0.10
      set-name: eth0
      routes:
      - to: default
        via: 192.168.1.1
        table: 200
      - to: 192.168.0.0/16
        table: 200
      routing-policy:
      - from: all
        to: 172.16.0.0/16
        table: 200
        priority: 0
      - from: all
        to: 172.24.0.0/16
        table: 200
        priority: 0
    eth1:
      accept-ra: false
      addresses:
      - 172.16.0.6/16
      dhcp4: false
      dhcp6: false
      match:
        macaddress: 00:50:56:99:05:c0
      nameservers:
        search:
        - dvm.lvn.broadcom.net
        - lvn.broadcom.net
        addresses:
        - 10.10.0.1
        - 10.10.0.2
      set-name: eth1
  version: 2

```

At boot, cloud-init renders the network configuration. The expected systemd-networkd DNS configuration is:
```ini
# /etc/systemd/network/10-cloud-init-eth0.network
[Match]
Name = eth0

[Network]
Address = 192.168.128.44/16
DNS = 172.24.0.10
Domains = ~cluster.local

# /etc/systemd/network/10-cloud-init-eth1.network
[Match]
Name = eth1

[Network]
Address = 172.16.0.6/16
DNS = 10.10.0.1,10.10.0.2
Domains = dvm.lvn.broadcom.net,lvn.broadcom.net
```
The expected `resolvectl` output is:
```
# resolvectl status
Global
         Protocols: -LLMNR +mDNS -DNSOverTLS DNSSEC=no/unsupported
  resolv.conf mode: stub

Link 2 (eth0)
    Current Scopes: DNS
         Protocols: -DefaultRoute -LLMNR -mDNS -DNSOverTLS DNSSEC=no/unsupported
Current DNS Server: 172.24.0.10
       DNS Servers: 172.24.0.10
        DNS Domain: ~cluster.local

Link 3 (eth1)
    Current Scopes: DNS
         Protocols: +DefaultRoute -LLMNR -mDNS -DNSOverTLS DNSSEC=no/unsupported
Current DNS Server: 10.10.0.1
       DNS Servers: 10.10.0.1 10.10.0.2
        DNS Domain: dvm.lvn.broadcom.net lvn.broadcom.net
```

The expected route table and rule output is:
```
# ip route show table 200
default via 192.168.1.1 dev eth0 proto static
192.168.0.0/16 dev eth0 proto static scope link

# ip rule
0:	from all lookup local
0:	from all to 172.16.0.0/16 lookup 200
0:	from all to 172.24.0.0/16 lookup 200
```

## VM Operator Dependencies

* [`nameservers`](https://github.com/vmware-tanzu/vm-operator/blob/release/vc-9.1.0/api/v1alpha5/virtualmachine_network_types.go#L160) and [`searchDomains`](https://github.com/vmware-tanzu/vm-operator/blob/release/vc-9.1.0/api/v1alpha5/virtualmachine_network_types.go#L181) already exist. When the user supplies `nameservers`, vm-operator must not override them. Expected priority: user-supplied > `SubnetPort`/`NetworkInterface` settings > global settings of the Supervisor workload network.
* Plumb `routes.table` and `routingPolicy` into the `VirtualMachine` v1alpha6 interface spec.
* Update `guestinfo.metadata` whenever the `VirtualMachine` interface spec changes (this is preparation for in-place update).
