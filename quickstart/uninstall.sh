#!/usr/bin/env bash
# Remove everything install.sh created.
set -uo pipefail
helm uninstall slurm -n slurm 2>/dev/null || true
helm uninstall slurm-operator -n slinky 2>/dev/null || true
helm uninstall slurm-operator-crds -n slinky 2>/dev/null || true
helm uninstall cert-manager -n cert-manager 2>/dev/null || true
kubectl delete namespace slurm slinky 2>/dev/null || true
echo "Uninstalled. (cert-manager namespace and CRDs left in place; remove manually if unused.)"
