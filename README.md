# Clusterra — Slurm on Kubernetes, batteries included

Run a real [Slurm](https://slurm.schedmd.com/) HPC cluster on Kubernetes, in your
own cloud account, with a **120-workload scientific catalog** that runs out of the
box — cryo-EM, molecular dynamics, genomics (Parabricks), proteomics, structure
prediction, Nextflow pipelines, and more.

This is the open **data plane** of Clusterra: the Helm charts, container images,
GitOps wiring, workload catalog, and node agent that turn a Kubernetes cluster into
a spot-friendly, single-queue Slurm cluster where heterogeneous CPU/GPU jobs share
one partition and nodes come and go with demand.

> **Status:** Clusterra was a managed-HPC product that has been wound down. This
> repository open-sources the data-plane stack under Apache 2.0 so the charts,
> images, and — especially — the **workload catalog** stay useful to the HPC and
> life-sciences community. The multi-tenant control plane (the autoscaling
> "brain" that computes node shapes and drives scale-up/down) is **not** included;
> see [What's here vs. not](#whats-here-vs-not). What ships here stands up a working
> cluster you scale yourself.

## Why it exists

Getting Slurm onto Kubernetes is a solved-but-tedious problem
([Slinky](https://github.com/SlinkyProject) from SchedMD is the operator underneath
this stack). The tedious part is everything *around* it: container images that
register cleanly with `slurmctld`, a partition layout that mixes CPU and GPU nodes
in one queue, spot-instance churn that leaves ghost nodes behind, and — the part
nobody ships — a library of scientific workloads that actually run. This repo is
that surrounding layer, extracted from a production system.

## What's in the box

| Path | What it is |
|------|-----------|
| [`catalog/`](catalog/) | **120 workload templates** (v2 schema) + the [template contract](catalog/schema/README.md). Each is a self-contained, documented sbatch/container recipe with a smoke fixture. This is the star of the release. |
| [`charts/`](charts/) | Helm charts for the cluster: the Slurm control plane (`cluster`), the worker/`slurmd` + login + node agent workloads (`customer-workloads`), Karpenter `NodePool`/`EC2NodeClass` (`customer-karpenter-config`), and the tenant secret scaffold. |
| [`images/`](images/) | Dockerfiles for `slurmctld`, `slurmd`, the Jupyter/Node-RED interactive images, and GROMACS. They layer thin patches on the public Slinky base images — build them yourself, no private registry needed. |
| [`edge-agent/`](edge-agent/) | The in-cluster node agent (Go). Reclaims **ghost nodes** (Karpenter workers whose `slurmd` never registered), reports node/price state, and actuates node-shape commands. Builds standalone (`go build ./...`). |
| [`deploy-argocd/`](deploy-argocd/), [`deploy-manifests/`](deploy-manifests/) | The ArgoCD app-of-apps and supporting manifests (cert-manager, Slinky operator + CRDs, MariaDB) that wire the whole thing together via GitOps. |

## Architecture in one paragraph

A Kubernetes cluster runs the Slinky Slurm operator. The `cluster` chart brings up
`slurmctld` + `slurmrestd` + accounting (MariaDB); `customer-workloads` brings up
`slurmd` as a DaemonSet on Karpenter-managed nodes, plus a login pod and the node
agent. Jobs land on a **single Slurm partition** that advertises multiple node
*shapes* (CPU, GPU, high-mem) via feasibility placeholders, so a heterogeneous mix
of jobs queues in one place. When jobs are pending, more nodes are requested from
Karpenter (spot by default); when they drain, nodes consolidate away. The node
agent keeps that loop honest by deleting nodes that came up broken. The catalog
sits on top: each template renders to an sbatch script that pulls a pinned
container and runs a real scientific tool.

## What's here vs. not

**Here (this repo, Apache 2.0):** the charts, the container images, the GitOps
manifests, the node agent, and the full workload catalog — enough to stand up a
Slurm-on-Kubernetes cluster and run real workloads on it, scaling nodes yourself
(e.g. by setting Slurm partition sizes / Karpenter limits, or wiring your own
scale logic to the node agent).

**Not here:** the central, multi-tenant control plane — the service that computed
optimal node *shapes* from the pending queue and drove automatic scale-up/down
across clusters, plus billing/metering and the web console. The node agent in this
repo is the *actuator* for that brain; run without it, the agent still does ghost
reclamation and reporting but will not autoscale on its own. Its `edgeAgent.enabled`
flag is therefore **off by default** in the chart values — turn it on only if you
point it at your own controller.

## Prerequisites

To bring the full reference architecture up you need, on the target cluster:

- A Kubernetes cluster (built and tested on **EKS**; the charts assume Karpenter).
- [Karpenter](https://karpenter.sh/) for node provisioning (spot-capable).
- An [ArgoCD](https://argo-cd.readthedocs.io/) install if you use the GitOps path
  in [`deploy-argocd/`](deploy-argocd/) (the `cluster` chart emits ArgoCD
  `Application` objects; you can also install the underlying Slinky `slurm` chart
  directly if you prefer plain Helm).
- A shared filesystem (the workloads assume an **EFS** mount at `/mnt/efs`).
- Two secrets in the `slurm` namespace — `clusterra-slurm-key` (`slurm.key`) and
  `clusterra-jwt-key` (`jwt.key`) — the externally-managed Slurm auth keys the
  operator consumes (see [`charts/cluster/values.yaml`](charts/cluster/values.yaml)).

## Quick start

```bash
# 1. Build and push the images to a registry your cluster can pull from
cd images/clusterra-slurmctld && docker build -t <your-registry>/slurmctld:local .
cd ../clusterra-slurmd     && docker build -t <your-registry>/slurmd:local .
# (repeat for the interactive images you want)

# 2. Install the Slinky operator + CRDs (see deploy-argocd/ or Slinky's docs)

# 3. Bring up the Slurm control plane
helm install my-cluster charts/cluster \
  --set cluster.name=my-cluster \
  --set slurm.controller.nodePort=30817 \
  --set slurm.efs.mountTargetIp=<efs-ip>

# 4. Bring up workers + catalog-ready login node
helm install my-workloads charts/customer-workloads \
  --set clusterId=my-cluster \
  --set slurmctldHost=<slurmctld-ip>:6817 \
  --set efs.enabled=true --set efs.mountTargetIp=<efs-ip>
```

This is a **reference architecture**, not a one-command SaaS installer — expect to
supply the cluster, Karpenter config, and shared filesystem for your environment.
The charts and manifests document the wiring that made it work in production.

## The catalog

The most reusable thing here even if you never run the cluster. Every template in
[`catalog/definitions/`](catalog/definitions/) declares its container, resources,
inputs/outputs, and a runnable smoke test against the
[v2 schema](catalog/schema/template.v2.schema.json). Domains include structure
prediction (Boltz, RFdiffusion, AlphaFold-family), cryo-EM (RELION, cryoSPARC-style
tools, CTFFIND), molecular dynamics & docking (GROMACS, AMBER, AutoDock-GPU,
OpenMM), genomics (Parabricks, Sarek), proteomics (Casanovo, MetaMorpheus),
metabolomics, phylogenetics, and quantum chemistry. Read one — e.g.
[`catalog/definitions/hcls/metabolomics/mzmine.yaml`](catalog/definitions/hcls/metabolomics/mzmine.yaml)
— and you have the pattern.

## License

[Apache License 2.0](LICENSE). Copyright the Clusterra authors.

Slurm is a trademark of SchedMD LLC. This project builds on the open-source
[Slinky](https://github.com/SlinkyProject) operator and is not affiliated with or
endorsed by SchedMD.
