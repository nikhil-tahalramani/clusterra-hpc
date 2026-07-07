package main

import (
	"context"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// reclaimGhostNodes detects K8s worker nodes whose slurmd never registered
// with slurmctld and deletes their Karpenter NodeClaims. Symmetric to
// central's `sweepOrphanSlurmNodes` — central catches Slurm-side ghosts
// (Slurm node with no K8s counterpart); this catches K8s-side ghosts (K8s
// node with no Ready slurmd, so Slurm has no row to detect from).
//
// Failure modes this catches: ImagePullBackOff on slurmd, GPU
// misclassification (slurmd entrypoint refuses to register), slurmctld
// unreachable during bootstrap, kubelet wedge.
//
// Plain List in the existing 15s cycle — no informers, no separate
// goroutine, no factory. Reuses agent's existing nodes/list and pods/list
// permissions plus nodeclaims/delete (used only here under v1.2; the
// graceful scale-down path went through `WhenEmpty` consolidation).
func reclaimGhostNodes(ctx context.Context, cs kubernetes.Interface, dyn dynamic.Interface, claims []nodeClaim, namespace string, ghostTimeout time.Duration) {
	now := time.Now()

	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "karpenter.sh/nodepool",
	})
	if err != nil {
		slog.Warn("ghost detector: list nodes failed", "err", err)
		return
	}

	slurmdPods, err := cs.CoreV1().Pods("slurm").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=slurmd",
	})
	if err != nil {
		slog.Warn("ghost detector: list slurmd pods failed", "err", err)
		return
	}

	readyByNode := make(map[string]bool, len(slurmdPods.Items))
	for i := range slurmdPods.Items {
		p := &slurmdPods.Items[i]
		if p.Spec.NodeName == "" {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				readyByNode[p.Spec.NodeName] = true
				break
			}
		}
	}

	for i := range nodes.Items {
		n := &nodes.Items[i]
		if now.Sub(n.CreationTimestamp.Time) <= ghostTimeout {
			continue
		}
		if readyByNode[n.Name] {
			continue
		}
		if err := deleteNodeClaim(ctx, dyn, claims, n.Name); err != nil {
			slog.Warn("ghost detector: delete nodeclaim failed", "node", n.Name, "err", err)
			continue
		}
		slog.Info("ghost detector: reclaimed K8s node with no Ready slurmd", "node", n.Name, "age", now.Sub(n.CreationTimestamp.Time))
	}
}
