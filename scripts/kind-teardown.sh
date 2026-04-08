#!/usr/bin/env bash
# Tear down everything deployed by kind-deploy.sh, leaving the kind cluster intact.
# Run from the control-loop/ repo root.

set -euo pipefail

echo "==> Deleting workload namespace (infer)"
kubectl delete namespace infer --ignore-not-found

echo "==> Deleting inferno namespace (inferno)"
kubectl delete namespace inferno --ignore-not-found

echo "==> Deleting cluster-scoped RBAC (ClusterRole + ClusterRoleBinding)"
kubectl delete clusterrolebinding inferno --ignore-not-found
kubectl delete clusterrole inferno --ignore-not-found

echo ""
echo "==> Done. Kind cluster is still running."
