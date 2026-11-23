# Examples

Run `mt` as a normal user. The examples use the configured default MicroShift image.

## Prepare access

Run this once per host.

```console
# Linux only. macOS uses a rootful Podman machine instead.
mt ops sudoers create
```

Inspect the CLI build and embedded defaults.

The examples below were verified with this build and configuration:

```console
$ mt -v
mt version dev (none, 2026-10-24T12:16:49Z)

$ mt config
{
  "name": "micro",
  "image": "ghcr.io/leelavg/microshift:5.0.0_202609201132_g4582bf673_5.0.0_okd_scos.ec.8",
  "powerTuning": true,
  "apiServerPort": 6443,
  "clusterCIDR": "10.42.0.0/16",
  "serviceCIDR": "10.43.0.0/16",
  "exposeKubeAPI": true,
  "lvmVolSize": "10G",
  "overprovisionRatio": 20,
  "shareHostContainers": true,
  "enableThinpool": true,
  "enableTopoLVM": true,
  "useEtcd": false,
  "bridgeName": "micro",
  "bridgeSubnet": "172.20.0.0/24",
  "groupCount": 5,
  "clustersPerGroup": 5,
  "reservePerGroup": 5,
  "nadNamespace": "default"
}
```

## Create and inspect a cluster

```console
mt cluster create \
  --name demo \
  --no-topolvm

mt cluster list
mt cluster config --name demo
mt node config --cluster demo
mt cluster kubeconfig --name demo >/tmp/demo-kubeconfig
export KUBECONFIG=/tmp/demo-kubeconfig

kubectl get nodes -o wide
kubectl get pods -A
```

Stop and start the cluster without removing its state.

```console
mt cluster stop --name demo
mt cluster start --name demo
```

## Use a shared local S3 service

Create a cluster for this example, then start the standalone service on
mt's default bridge network. It does not publish a host port.

```console
mt cluster create --name s3-demo --no-topolvm
mt addon local-s3 install
mt addon local-s3 configure --attach-to s3-demo

mt cluster kubeconfig --name s3-demo >/tmp/s3-demo-kubeconfig
kubectl --kubeconfig /tmp/s3-demo-kubeconfig get service s3 -n default
```

The endpoint inside the cluster is `http://s3.default.svc:9000`. No
accounts are configured, so requests are anonymous. mt does not create an
access key or secret. If a client requires a region, use `us-east-1`. For the
AWS CLI, use `--no-sign-request`:

```console
aws --no-sign-request --endpoint-url http://s3.default.svc:9000 s3 ls
```

Uninstalling preserves the data volume. Pass `--cleanup` to delete it.

```console
mt addon local-s3 uninstall
```

To also delete the data volume:

```console
mt addon local-s3 uninstall --cleanup
```

## Add and remove a worker

Create a cluster for this example. Add a worker after the control plane is
ready. The command checks control-plane readiness before onboarding the worker.

```console
mt cluster create --name worker-demo --no-topolvm
mt node add --cluster worker-demo
mt cluster kubeconfig --name worker-demo >/tmp/worker-demo-kubeconfig
export KUBECONFIG=/tmp/worker-demo-kubeconfig
kubectl get nodes -o wide
kubectl get pods -A -o wide
sudo podman exec worker-demo-1 journalctl -u microshift --no-pager
```

Add explicit mounts to a worker when it needs storage not present in the cluster
creation mounts.

```console
mt node add --cluster worker-demo --mount /host/path:/worker/path:ro
```

Use `--force` when debugging worker onboarding and the pre-add readiness check is not desired.

```console
mt node add --cluster worker-demo --force
```

Remove a worker and add another one to reuse the freed slot.

```console
mt node remove --cluster worker-demo --name worker-demo-1
mt node add --cluster worker-demo
kubectl get nodes -o wide
```

## Create an etcd-backed cluster

```console
mt cluster create \
  --name etcd-demo \
  --etcd \
  --no-topolvm

mt node add --cluster etcd-demo
mt cluster kubeconfig --name etcd-demo >/tmp/etcd-demo-kubeconfig
KUBECONFIG=/tmp/etcd-demo-kubeconfig kubectl get nodes
```

## Peer two clusters

Use distinct pod and service CIDRs when testing peer operations.

```console
mt cluster create \
  --name net-a \
  --no-topolvm \
  --api-server-port 6443 \
  --cluster-cidr 10.42.0.0/16 \
  --service-cidr 10.43.0.0/16
mt cluster create \
  --name net-b \
  --no-topolvm \
  --api-server-port 6444 \
  --cluster-cidr 10.52.0.0/16 \
  --service-cidr 10.53.0.0/16

mt network peer --cluster net-a --cluster net-b
mt network unpeer --cluster net-a --cluster net-b
```

## Attach clusters to a shared secondary network

Create two clusters for this example, then create one Podman bridge and connect
both clusters to it. The secondary network uses its own subnet and does not
replace the cluster or service networks.

```console
mt cluster create --name attach-a --no-topolvm --api-server-port 6443
mt cluster create --name attach-b --no-topolvm --api-server-port 6444
mt network create --name backbone --subnet 172.30.0.0/16
mt network connect --cluster attach-a --cluster attach-b --to backbone

mt network attach \
  --cluster attach-a:group1 \
  --cluster attach-b:group1 \
  --to backbone \
  --multi-node
```

With `--multi-node`, the attachment uses Whereabouts IPAM and enables it on the cluster nodes.

Deploy a diagnostic pod in each cluster and inspect the assigned secondary addresses.

```console
mt cluster kubeconfig --name attach-a >/tmp/attach-a-kubeconfig
mt cluster kubeconfig --name attach-b >/tmp/attach-b-kubeconfig

kubectl --kubeconfig /tmp/attach-a-kubeconfig apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: netshoot-a
  annotations:
    k8s.v1.cni.cncf.io/networks: default/backbone-group1
spec:
  containers:
  - name: netshoot
    image: docker.io/nicolaka/netshoot:v0.16
    command: ["sleep", "infinity"]
    securityContext:
      capabilities:
        add: ["NET_RAW"]
EOF

kubectl --kubeconfig /tmp/attach-b-kubeconfig apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: netshoot-b
  annotations:
    k8s.v1.cni.cncf.io/networks: default/backbone-group1
spec:
  containers:
  - name: netshoot
    image: docker.io/nicolaka/netshoot:v0.16
    command: ["sleep", "infinity"]
    securityContext:
      capabilities:
        add: ["NET_RAW"]
EOF

kubectl --kubeconfig /tmp/attach-a-kubeconfig get pod netshoot-a -o wide
kubectl --kubeconfig /tmp/attach-b-kubeconfig get pod netshoot-b -o wide
```

Inspect and remove the attachment when finished.

```console
kubectl --kubeconfig /tmp/attach-a-kubeconfig delete pod netshoot-a --grace-period=10
kubectl --kubeconfig /tmp/attach-b-kubeconfig delete pod netshoot-b --grace-period=10
mt network config --name backbone
mt network detach --cluster attach-a:group1 --cluster attach-b:group1 --from backbone
mt network disconnect --cluster attach-a --cluster attach-b --from backbone
mt network delete --name backbone
```

## Configure ODF storage topologies

Each scenario creates its own clusters. Add workers to a provider before
configuring it as a multi-node provider.

Load the host kernel modules before installing ODF operators. The image below
is an example tag. Replace it with an image available to your environment.
If images pulled by a cluster require registry credentials, pass
`--pull-secret` when creating that cluster. If resolving its image references
requires IDMS rules, pass `--idms` as well. Add the applicable flags to each
cluster creation command in the scenario you run.

### Single-node ODF

Create a single-node cluster, then install and configure ODF:

```console
mt cluster create --name standalone
mt addon odf modules load
mt addon odf --cluster standalone install \
  --catalog-image quay.io/example/odf-operator-catalog:v5.0 \
  --channel stable-5.0 --version 5.0.0
mt addon odf --cluster standalone configure

mt cluster kubeconfig --name standalone >/tmp/standalone-kubeconfig
kubectl --kubeconfig /tmp/standalone-kubeconfig get storagecluster -n openshift-storage
kubectl --kubeconfig /tmp/standalone-kubeconfig get storageclass
mt cluster rm --name standalone
```

### Rook Ceph provider with an external ODF consumer

Create two clusters, then install the Rook operator on `rook-provider` and the
ODF operator on `rook-consumer`:

```console
mt cluster create --name rook-provider --api-server-port 6443
mt cluster create --name rook-consumer --api-server-port 6444
mt addon odf modules load
mt addon odf --cluster rook-provider install \
  --catalog-image quay.io/example/odf-operator-catalog:v5.0 \
  --channel stable-5.0 --version 5.0.0 --sub-name rook-ceph-operator
mt addon odf --cluster rook-consumer install \
  --catalog-image quay.io/example/odf-operator-catalog:v5.0 \
  --channel stable-5.0 --version 5.0.0 --sub-name odf-operator
```

Configure Rook on `rook-provider`, then connect `rook-consumer` to it:

```console
mt addon odf --cluster rook-provider configure --external-ceph
mt addon odf --cluster rook-consumer configure --connect-to rook-provider

mt cluster kubeconfig --name rook-provider >/tmp/rook-provider-kubeconfig
mt cluster kubeconfig --name rook-consumer >/tmp/rook-consumer-kubeconfig
kubectl --kubeconfig /tmp/rook-provider-kubeconfig get cephcluster -n openshift-storage
kubectl --kubeconfig /tmp/rook-consumer-kubeconfig get storagecluster -n openshift-storage
```

### ODF provider with a client-only consumer

Create two clusters, then install the ODF operator on `odf-provider` and the
client operator on `odf-consumer`:

```console
mt cluster create --name odf-provider --api-server-port 6443 \
  --cluster-cidr 10.42.0.0/16 --service-cidr 10.43.0.0/16
mt cluster create --name odf-consumer --api-server-port 6444 \
  --cluster-cidr 10.52.0.0/16 --service-cidr 10.53.0.0/16
mt addon odf modules load
mt addon odf --cluster odf-provider install \
  --catalog-image quay.io/example/odf-operator-catalog:v5.0 \
  --channel stable-5.0 --version 5.0.0 --sub-name odf-operator
mt addon odf --cluster odf-consumer install \
  --catalog-image quay.io/example/odf-operator-catalog:v5.0 \
  --channel stable-5.0 --version 5.0.0 --sub-name ocs-client-operator
```

Choose one provider network mode. For a single-node provider without host
networking, peer the clusters before onboarding the client:

```console
mt addon odf --cluster odf-provider configure
mt network peer --cluster odf-provider --cluster odf-consumer
```

For a host-networked provider, add workers before configuring it:

```console
# Host-networked Ceph MONs need separate nodes, so use at least three nodes.
mt node add --cluster odf-provider
mt node add --cluster odf-provider
mt addon odf --cluster odf-provider configure --hostnetwork --multi-node
```

With either mode configured, onboard the client-only consumer:

```console
mt addon odf --cluster odf-consumer configure --client --connect-to odf-provider

mt cluster kubeconfig --name odf-provider >/tmp/odf-provider-kubeconfig
mt cluster kubeconfig --name odf-consumer >/tmp/odf-consumer-kubeconfig
kubectl --kubeconfig /tmp/odf-provider-kubeconfig patch storageconsumer odf-consumer \
  -n openshift-storage --type=json \
  -p '[{"op":"add","path":"/spec/storageClasses","value":[{"name":"ocs-storagecluster-ceph-rbd"}]}]'
kubectl --kubeconfig /tmp/odf-consumer-kubeconfig get storageclass
```

Create workloads in `odf-consumer` using the published `ocs-storagecluster-ceph-rbd`
StorageClass to verify client-side provisioning and access.
