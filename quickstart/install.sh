#!/usr/bin/env bash
# Launch a real, multi-node Slurm cluster on any Kubernetes cluster.
#
#   ./quickstart/install.sh
#
# Works against whatever cluster your current kubectl context points at —
# a local kind/minikube cluster or a real EKS cluster. Takes ~3 minutes.
# Idempotent: safe to re-run.
#
# Requires: kubectl, helm (v3.8+ for OCI), and a reachable cluster.
set -euo pipefail

# Pin the whole Slinky stack to one aligned version. operator, CRDs, and the
# slurm chart MUST match (they share the slinky.slurm.net CRD schema).
SLINKY_VERSION="${SLINKY_VERSION:-1.1.0}"
VALUES="$(cd "$(dirname "$0")" && pwd)/values.yaml"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$1"; }

say "1/4  cert-manager (Slinky operator dependency)"
helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true --wait --timeout 5m

say "2/4  Slinky CRDs ($SLINKY_VERSION)"
# CRDs must exist before the operator and the cluster chart reference them.
helm upgrade --install slurm-operator-crds \
  oci://ghcr.io/slinkyproject/charts/slurm-operator-crds \
  --version "$SLINKY_VERSION" --namespace slinky --create-namespace

say "3/4  Slinky operator ($SLINKY_VERSION)"
helm upgrade --install slurm-operator \
  oci://ghcr.io/slinkyproject/charts/slurm-operator \
  --version "$SLINKY_VERSION" --namespace slinky --wait --timeout 5m

say "4/4  Slurm cluster ($SLINKY_VERSION)"
helm upgrade --install slurm \
  oci://ghcr.io/slinkyproject/charts/slurm \
  --version "$SLINKY_VERSION" --namespace slurm --create-namespace \
  -f "$VALUES" --timeout 8m

say "Waiting for the controller to be ready..."
kubectl wait --for=jsonpath='{.status.phase}'=Running pod/slurm-controller-0 \
  -n slurm --timeout=5m 2>/dev/null || true
kubectl rollout status statefulset/slurm-controller -n slurm --timeout=5m || true

say "Waiting for a worker node to register with Slurm..."
# The NodeSet controller creates slurmd pods after the cluster CR reconciles,
# and slurmd registers a few seconds after its pod is Ready. On a fresh cluster
# the first slurmd image pull can take a couple of minutes. Block until at
# least one node is in an available state so the first `sinfo` shows compute.
registered=0
for _ in $(seq 1 90); do   # up to ~7.5 min
  n=$(kubectl exec -n slurm slurm-controller-0 -c slurmctld -- \
        sinfo -h -N -t idle,alloc,mix 2>/dev/null | grep -c . || true)
  if [ "${n:-0}" -ge 1 ]; then registered=1; break; fi
  sleep 5
done

if [ "$registered" = 1 ]; then
  cat <<'EOF'

  ✅ Slurm is up, with compute registered. Try it:

     kubectl exec -n slurm slurm-controller-0 -c slurmctld -- sinfo
     kubectl exec -n slurm slurm-controller-0 -c slurmctld -- srun -N1 hostname

  Tear it all down with:  ./quickstart/uninstall.sh
EOF
else
  cat <<'EOF'

  ⚠️  Control plane is up, but no worker registered yet — the slurmd image is
     probably still pulling. Watch it finish, then run sinfo:

     kubectl get pods -n slurm -w        # wait for slurm-worker-* to reach 2/2
     kubectl exec -n slurm slurm-controller-0 -c slurmctld -- sinfo

  Tear it all down with:  ./quickstart/uninstall.sh
EOF
fi
