# Clusterra — a real Slurm cluster on Kubernetes, in one command

Launch an actual multi-node [Slurm](https://slurm.schedmd.com/) HPC cluster on any
Kubernetes cluster — a laptop [kind](https://kind.sigs.k8s.io/) cluster or your own
**EKS** account — and start submitting jobs in about three minutes:

```bash
git clone https://github.com/nikhil-tahalramani/clusterra-hpc
cd clusterra-hpc
./quickstart/install.sh          # points at your current kubectl context
```

Then run jobs, for real:

```console
$ kubectl exec -n slurm slurm-controller-0 -c slurmctld -- sinfo
PARTITION AVAIL  TIMELIMIT  NODES  STATE NODELIST
all*         up   infinite      1   idle slinky-0

$ kubectl exec -n slurm slurm-controller-0 -c slurmctld -- srun -N1 hostname
slinky-0
```

That's a live `slurmctld`, `slurmd` workers, and `slurmrestd` running as pods, with
the Slurm auth keys generated for you — no shared filesystem, no cloud autoscaler,
no external secrets required to get started. On EKS with N nodes you get N workers;
add more by bumping one number in [`quickstart/values.yaml`](quickstart/values.yaml).

> The quickstart is an opinionated, one-command wrapper over SchedMD's open-source
> [Slinky](https://github.com/SlinkyProject) operator — it's the on-ramp. The rest
> of this repo is what turns that base cluster into the production system Clusterra
> ran: **spot-instance autoscaling, a single queue for mixed CPU/GPU workloads, and
> a 120-workload scientific catalog that runs out of the box.**

## Level up: the production stack

The quickstart gets you a working cluster. The [`charts/`](charts/),
[`images/`](images/), [`catalog/`](catalog/), and [`edge-agent/`](edge-agent/)
directories are the pieces that made Clusterra a managed-HPC product:

| Path | What it adds |
|------|-----------|
| [`catalog/`](catalog/) | **120 workload templates** (v2 schema) — cryo-EM, molecular dynamics & docking, genomics (Parabricks), proteomics, structure prediction (Boltz, RFdiffusion), Nextflow pipelines, metabolomics, quantum chemistry. Each is a self-contained, documented sbatch/container recipe with a smoke test. Reusable even if you never run the cluster. |
| [`charts/`](charts/) | The production Helm charts: single-queue partition layout that mixes CPU/GPU nodes, `slurmd` on [Karpenter](https://karpenter.sh/)-managed **spot** nodes, EFS-backed shared storage, login node wired for the catalog. |
| [`images/`](images/) | Dockerfiles for `slurmctld`/`slurmd` (with the dynamic-user patch that lets Slurm resolve on-the-fly accounts) and the interactive Jupyter/Node-RED/GROMACS images. They layer thin patches on the public Slinky bases — build them yourself, no private registry. |
| [`edge-agent/`](edge-agent/) | The in-cluster node agent (Go). Reclaims **ghost nodes** — Karpenter workers whose `slurmd` never registered (image-pull failures, GPU misclassification) — and reports node/price state. Builds standalone. |
| [`deploy-argocd/`](deploy-argocd/), [`deploy-manifests/`](deploy-manifests/) | The ArgoCD app-of-apps and supporting manifests to run the whole thing GitOps-style. |

## How the pieces fit

A Kubernetes cluster runs the Slinky Slurm operator. The quickstart brings up a
self-contained cluster in-pods. The production charts extend that: `slurmd` runs as
a DaemonSet on Karpenter nodes, jobs land on **one Slurm partition** that advertises
multiple node *shapes* (CPU, GPU, high-mem) so a heterogeneous mix of jobs queues in
one place, spot nodes scale up on pending demand and consolidate away when idle, and
the node agent deletes any node that came up broken. The catalog sits on top: each
template renders to an sbatch script that pulls a pinned container and runs a real
scientific tool.

## What's here vs. not

**Here (Apache 2.0):** the one-command quickstart, the production charts, the
container images, the GitOps manifests, the node agent, and the full workload
catalog — enough to run a real Slurm cluster and real scientific workloads, and to
reproduce the spot/heterogeneous-queue topology.

**Not here:** the central, multi-tenant control plane that computed optimal node
*shapes* from the pending queue and drove automatic scale-up/down across many
clusters, plus billing/metering and the web console. The node agent here is the
*actuator* for that brain; on its own it does ghost reclamation and reporting but
does not autoscale. Its `edgeAgent.enabled` flag is therefore off by default —
enable it only if you point it at your own controller.

> **Background:** Clusterra was a managed-HPC product that has been wound down. This
> repository open-sources the data-plane stack so the charts, images, and — above
> all — the workload catalog stay useful to the HPC and life-sciences community.

## The catalog

The most reusable thing here even if you never run the cluster. Every template in
[`catalog/definitions/`](catalog/definitions/) declares its container, resources,
inputs/outputs, and a runnable smoke test against the
[v2 schema](catalog/schema/template.v2.schema.json). Read one — e.g.
[`catalog/definitions/hcls/metabolomics/mzmine.yaml`](catalog/definitions/hcls/metabolomics/mzmine.yaml)
— and you have the pattern.

## Requirements

- **Quickstart:** `kubectl`, `helm` (v3.8+, for OCI charts), and any reachable
  Kubernetes cluster. For a local try: `kind create cluster` first.
- **Production stack:** additionally Karpenter (spot node provisioning) and a shared
  filesystem (EFS) mounted at `/mnt/efs` for the catalog workloads.

Tear the quickstart down with [`./quickstart/uninstall.sh`](quickstart/uninstall.sh).

## License

[Apache License 2.0](LICENSE). Copyright the Clusterra authors.

Slurm is a trademark of SchedMD LLC. This project builds on the open-source
[Slinky](https://github.com/SlinkyProject) operator and is not affiliated with or
endorsed by SchedMD.
