package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ── Config ───────────────────────────────────────────────────────────────────

type config struct {
	centralAPIURL string
	agentAPIKey   string
	clusterID     string
	namespace     string
	pollInterval  time.Duration
	// Ghost-node TTL: a Karpenter worker older than this with no Ready
	// slurmd pod is considered a ghost (image-pull failure, GPU
	// misclassification, etc.) and reclaimed via NodeClaim deletion.
	ghostTimeout time.Duration
}

func configFromEnv() (config, error) {
	c := config{
		centralAPIURL: os.Getenv("CLUSTERRA_CENTRAL_API_URL"),
		agentAPIKey:   os.Getenv("CLUSTERRA_AGENT_API_KEY"),
		clusterID:     os.Getenv("CLUSTERRA_CLUSTER_ID"),
		namespace:     getEnvOrDefault("CLUSTERRA_NAMESPACE", "slurm"),
	}
	if c.centralAPIURL == "" || c.clusterID == "" {
		return c, fmt.Errorf("CLUSTERRA_CENTRAL_API_URL and CLUSTERRA_CLUSTER_ID are required")
	}
	secs, _ := strconv.Atoi(getEnvOrDefault("CLUSTERRA_POLL_INTERVAL", "15"))
	if secs < 1 {
		secs = 15
	}
	c.pollInterval = time.Duration(secs) * time.Second

	ghostSecs, _ := strconv.Atoi(getEnvOrDefault("CLUSTERRA_GHOST_TIMEOUT_SECONDS", "480"))
	if ghostSecs < 60 {
		ghostSecs = 480
	}
	c.ghostTimeout = time.Duration(ghostSecs) * time.Second

	return c, nil
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── API types ────────────────────────────────────────────────────────────────

type capacityInfo struct {
	Count int32 `json:"count"`
}

type heartbeatRequest struct {
	ClusterID          string                  `json:"cluster_id"`
	AgentVersion       string                  `json:"agent_version"`
	CapacityPods       map[string]capacityInfo `json:"capacity_pods"`
	LastAppliedVersion int                     `json:"last_applied_version"`
	SlurmKeyHash       string                  `json:"slurm_key_hash,omitempty"`
	AgentIP            string                  `json:"agent_ip,omitempty"`
	// K8sNodes is the current list of Karpenter worker node names in the
	// customer cluster. Central uses this to detect Slurm nodes with no
	// matching K8s node (ghosts from ungraceful termination) and sweep them.
	//
	// No omitempty: an empty slice means "agent successfully listed and
	// there are zero workers" and must serialize as `[]`, distinct from
	// `null` which means "agent couldn't list" (central skips the sweep).
	K8sNodes []string `json:"k8s_nodes"`
	// NodeClaims is the local Karpenter NodeClaim list, listed against the
	// dynamic client. Authoritative for cluster billing on the central side.
	// nil = list failed (central skips burn calc this cycle); empty = listed
	// successfully and there are zero NodeClaims (zero burn).
	NodeClaims []nodeClaim `json:"nodeclaims"`
	// Prices is the per-(type, lifecycle, zone) price table scraped from the
	// in-cluster Karpenter metrics endpoint. nil/empty = scrape failed; central
	// holds the previous snapshot until it goes stale (90s).
	Prices []priceQuote `json:"prices,omitempty"`
}

type heartbeatResponse struct {
	Ack                    bool           `json:"ack"`
	ScalingCommands        []shapeCommand `json:"scaling_commands"`
	ScalingCommandsVersion int            `json:"scaling_commands_version"`
	SlurmKey               string         `json:"slurm_key,omitempty"`
	// ProtectedNodes lists worker nodes currently running Slurm jobs.
	// Drives two behaviors locally:
	//   1. Annotate the K8s Node with `karpenter.sh/do-not-disrupt=true`
	//      while in the list; remove the annotation when it leaves.
	//   2. When deleting surplus cap-pods (existing > desired), prefer pods
	//      bound to nodes NOT in this list.
	ProtectedNodes []string `json:"protected_nodes,omitempty"`
}

const agentVersion = "5.0.0"

// ── Shape transport ─────────────────────────────────────────────────────────

// shapeSpec mirrors scaling.ShapeSpec on the central side. Both binaries
// independently encode/decode this same JSON shape — no shared package, but
// the JSON tag set is the wire contract. Cap-pod construction reads only
// shapeSpec; the legacy parseShapeName path is gone.
type shapeSpec struct {
	Arch     string `json:"arch"`
	VCPUs    int    `json:"vcpus"`
	MemGiB   int    `json:"mem_gib"`
	GPUCount int    `json:"gpu_count,omitempty"`
	GPUModel string `json:"gpu_model,omitempty"`
	// TmpMiB is the job's --tmp request in MiB. When > 0 the cap-pod
	// requests `ephemeral-storage: <TmpMiB>Mi` so Karpenter's binpacker
	// (with EC2NodeClass instanceStorePolicy=RAID0 + kubelet data-dir on
	// /mnt/k8s-disks/0) narrows to instance-store families large enough to
	// cover it. Replaces the prior `NVMe bool` + instance-local-nvme>0
	// nodeAffinity (label routing only, no budget).
	TmpMiB   int  `json:"tmp_mib,omitempty"`
	OnDemand bool `json:"on_demand,omitempty"`
}

// shapeCommand mirrors scaling.ShapeCommand: per-shape replica directive sent
// from cluster-api over heartbeat.
type shapeCommand struct {
	Name     string    `json:"name"`
	Replicas int32     `json:"replicas"`
	Shape    shapeSpec `json:"shape"`
}

// ── Capacity-request pod helpers ────────────────────────────────────────────

const (
	labelCapacityRequest = "clusterra.io/capacity-request"
	labelShape           = "clusterra.io/shape"
	// GPU node isolation. Every node Karpenter labels with
	// instance-gpu-manufacturer=nvidia gets this taint applied by
	// reconcileGPUTaints. Only GPU cap-pods + GPU-targeting DaemonSets
	// (slurmd, nvidia-device-plugin) carry the matching toleration, so the
	// K8s scheduler cannot binpack a CPU cap-pod onto a live GPU node.
	gpuTaintKey   = "clusterra.io/gpu"
	gpuTaintValue = "true"
	gpuNodeLabel  = "karpenter.k8s.aws/instance-gpu-manufacturer"

	// gpuNotReadyTaintKey is applied by kubelet --register-with-taints on
	// every fresh GPU node (see ec2nodeclass.yaml userData) and removed by
	// reconcileGPUReadyTaint once nvidia-device-plugin has advertised
	// nvidia.com/gpu as Allocatable. Closes the device-plugin-bootstrap
	// race that otherwise lets cap-pods schedule onto a node before the
	// GPU resource is visible to the scheduler.
	gpuNotReadyTaintKey = "nvidia.com/gpu"
	gpuNotReadyValue    = "not-ready"
	nvidiaGPUResource   = "nvidia.com/gpu"
)

func listCapacityPods(ctx context.Context, cs kubernetes.Interface, namespace string) ([]corev1.Pod, error) {
	list, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelCapacityRequest + "=true",
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func countByShape(pods []corev1.Pod) map[string]int32 {
	counts := make(map[string]int32)
	for _, p := range pods {
		shapeName := p.Labels[labelShape]
		if shapeName != "" {
			counts[shapeName]++
		}
	}
	return counts
}

// buildCapacityPod constructs a capacity-request pod spec for the given
// ShapeCommand. Reads only cmd.Shape — the shape name is K8s-object naming
// only, never reverse-parsed. Pause containers whose sole purpose is to
// reserve resources and trigger Karpenter provisioning.
func buildCapacityPod(namespace string, cmd shapeCommand) (*corev1.Pod, error) {
	s := cmd.Shape
	name := fmt.Sprintf("cap-%s-%s", cmd.Name, randomSuffix(5))

	cpuQty := resource.MustParse(fmt.Sprintf("%d", s.VCPUs))
	memQty := resource.MustParse(fmt.Sprintf("%dGi", s.MemGiB))
	// Every dimension a Slurm job constrains on must reach Karpenter via
	// `resources.requests`, not labels. Labels remain for routing only
	// (arch, GPU model, capacity-type); count/size dimensions are budgeted.
	// See internal design notes.
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    cpuQty,
		corev1.ResourceMemory: memQty,
	}
	if s.GPUCount > 0 {
		// Requesting nvidia.com/gpu makes the K8s scheduler enforce
		// per-node GPU budget — two GPU cap-pods can no longer stack onto
		// the same node. The historical multi-node runaway from this
		// request is mitigated by (1) Karpenter v1.9 accounting for
		// in-flight NodeClaims, and (2) the not-ready kubelet startup
		// taint applied to GPU nodes that holds the cap-pod pending
		// until nvidia-device-plugin advertises the resource.
		requests["nvidia.com/gpu"] = *resource.NewQuantity(int64(s.GPUCount), resource.DecimalSI)
	}
	if s.TmpMiB > 0 {
		// ephemeral-storage on Karpenter workers reports the NVMe RAID0
		// array (via kubelet --data-dir=/mnt/k8s-disks/0/k3s in
		// ec2nodeclass userData). Binpacker narrows to families whose
		// Allocatable ephemeral-storage covers the request — no
		// instance-local-nvme nodeAffinity needed.
		requests[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(
			int64(s.TmpMiB)*1024*1024, resource.BinarySI)
	}
	// Limits mirror requests: cap-pods are pause containers, so values are
	// budget declarations, not enforcement targets.
	limits := make(corev1.ResourceList, len(requests))
	for k, v := range requests {
		limits[k] = v
	}
	resources := corev1.ResourceRequirements{
		Requests: requests,
		Limits:   limits,
	}
	nodeSelector := map[string]string{
		"kubernetes.io/arch": s.Arch,
	}
	// GPU routing labels remain for steering to the right family/model;
	// the actual count is now budgeted via the nvidia.com/gpu request above.
	if s.GPUCount > 0 {
		nodeSelector["karpenter.k8s.aws/instance-gpu-manufacturer"] = "nvidia"
		nodeSelector["karpenter.k8s.aws/instance-gpu-count"] = fmt.Sprintf("%d", s.GPUCount)
	}
	if s.GPUModel != "" {
		nodeSelector["karpenter.k8s.aws/instance-gpu-name"] = s.GPUModel
	}
	// On-demand routing (Phase 3). Selector key is Karpenter's well-known
	// capacity-type label; matched values are "spot" / "on-demand".
	if s.OnDemand {
		nodeSelector["karpenter.sh/capacity-type"] = "on-demand"
	}

	// No more `instance-local-nvme > 0` Gt-affinity — the
	// `ephemeral-storage` request is strictly more precise. Karpenter
	// narrows to NVMe-bearing instance-store families automatically
	// because EBS-root families don't cover large ephemeral-storage
	// requests.
	var affinity *corev1.Affinity

	tolerations := []corev1.Toleration{
		// Karpenter taint on fresh nodes before registration completes
		{Key: "karpenter.sh/unregistered", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
		// Cilium taint on fresh nodes before agent initializes (if Cilium is CNI)
		{Key: "node.cilium.io/agent-not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
	}
	// GPU cap-pods need to tolerate the clusterra.io/gpu taint that
	// reconcileGPUTaints applies to every GPU node. CPU cap-pods stay
	// untolerated, which is what stops the K8s scheduler from binpacking
	// them onto a live GPU node and starving the cpu Slurm partition.
	if s.GPUCount > 0 {
		tolerations = append(tolerations, corev1.Toleration{
			Key: gpuTaintKey, Operator: corev1.TolerationOpEqual, Value: "true", Effect: corev1.TaintEffectNoSchedule,
		})
		// The not-ready taint is applied by kubelet --register-with-taints
		// on every fresh GPU node and removed by reconcileGPUReadyTaint
		// once nvidia-device-plugin advertises nvidia.com/gpu Allocatable.
		// GPU cap-pods tolerate it so they can bind once the plugin has
		// registered — the K8s scheduler still won't actually bind until
		// nvidia.com/gpu Allocatable >= request, which is what gates the
		// historical multi-node runaway.
		tolerations = append(tolerations, corev1.Toleration{
			Key: gpuNotReadyTaintKey, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
		})
	}

	// No RuntimeClass: the cap pod runs `pause` — it just holds the node
	// for the slurmd DaemonSet pod to attach. It doesn't access the GPU,
	// so requesting RuntimeClass=nvidia would just add a dependency on a
	// RuntimeClass that may not yet exist on a fresh Karpenter node and
	// block scheduling unnecessarily.
	var runtimeClassName *string

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				labelCapacityRequest: "true",
				labelShape:           cmd.Name,
			},
			// No `karpenter.sh/do-not-disrupt` annotation: the node anchor
			// for nodes running Slurm jobs is set on the K8s Node itself by
			// reconcileNodeAnchors, driven from central's ProtectedNodes.
			// Cap-pods are now pure provisioning-trigger + capacity-reservation
			// objects; their lifecycle tracks `RUNNING + PENDING` per shape.
		},
		Spec: corev1.PodSpec{
			RuntimeClassName: runtimeClassName,
			NodeSelector:     nodeSelector,
			Affinity:         affinity,
			Tolerations:      tolerations,
			Containers: []corev1.Container{
				{
					Name:      "pause",
					Image:     "registry.k8s.io/pause:3.10",
					Resources: resources,
				},
			},
		},
	}

	return pod, nil
}

// reconcileGPUTaints ensures every GPU-labeled node carries the
// clusterra.io/gpu=true:NoSchedule taint. Karpenter sets the
// instance-gpu-manufacturer label on GPU nodes during registration; we add
// the taint on the next cycle so the K8s scheduler refuses to bind any
// pod that doesn't explicitly tolerate it. Idempotent — skips nodes that
// already have the taint.
func reconcileGPUTaints(ctx context.Context, cs kubernetes.Interface) {
	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: gpuNodeLabel + "=nvidia",
	})
	if err != nil {
		slog.Warn("list gpu nodes failed", "err", err)
		return
	}
	for i := range list.Items {
		n := &list.Items[i]
		already := false
		for _, t := range n.Spec.Taints {
			if t.Key == gpuTaintKey {
				already = true
				break
			}
		}
		if already {
			continue
		}
		taints := append(n.Spec.Taints, corev1.Taint{
			Key: gpuTaintKey, Value: gpuTaintValue, Effect: corev1.TaintEffectNoSchedule,
		})
		// JSON merge patch — replaces spec.taints atomically. Concurrent
		// edits would lose the previously-merged value, but Karpenter is
		// the only other writer (its startupTaints land before our
		// reconcile, so they're already in n.Spec.Taints we just read).
		patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"taints": taints}})
		if _, err := cs.CoreV1().Nodes().Patch(ctx, n.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			slog.Warn("apply gpu taint failed", "node", n.Name, "err", err)
			continue
		}
		slog.Info("applied gpu taint", "node", n.Name)
	}
}

// reconcileGPUReadyTaint removes the `nvidia.com/gpu=not-ready:NoSchedule`
// kubelet startup taint from any Karpenter worker whose status.allocatable
// already shows `nvidia.com/gpu >= 1` — i.e. nvidia-device-plugin has
// registered the resource. Idempotent and single-writer (the agent). Reuses
// the existing `nodes/patch` RBAC — no new permissions required.
//
// Why the agent does this: nvidia-device-plugin v0.17.0 tolerates the taint
// and runs on the tainted node, but it does NOT remove arbitrary node taints
// itself. Without this reconciler the not-ready taint would stay forever and
// the GPU cap-pod would sit Pending indefinitely.
//
// Window between plugin registration and the next agent tick is ≤15s (the
// poll interval). Cap-pod stays Pending in that window; no spurious node
// provisioning because Karpenter v1.9 accounts for the in-flight NodeClaim.
func reconcileGPUReadyTaint(ctx context.Context, cs kubernetes.Interface) {
	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: gpuNodeLabel + "=nvidia",
	})
	if err != nil {
		slog.Warn("reconcile gpu-ready taint: list nodes failed", "err", err)
		return
	}
	for i := range list.Items {
		n := &list.Items[i]

		// Is the not-ready taint still present?
		idx := -1
		for j, t := range n.Spec.Taints {
			if t.Key == gpuNotReadyTaintKey && t.Value == gpuNotReadyValue && t.Effect == corev1.TaintEffectNoSchedule {
				idx = j
				break
			}
		}
		if idx < 0 {
			continue
		}

		// Has the device plugin advertised the resource yet?
		qty, ok := n.Status.Allocatable[corev1.ResourceName(nvidiaGPUResource)]
		if !ok || qty.IsZero() {
			continue
		}

		newTaints := append([]corev1.Taint{}, n.Spec.Taints[:idx]...)
		newTaints = append(newTaints, n.Spec.Taints[idx+1:]...)
		patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"taints": newTaints}})
		if _, err := cs.CoreV1().Nodes().Patch(ctx, n.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			slog.Warn("remove gpu not-ready taint failed", "node", n.Name, "err", err)
			continue
		}
		slog.Info("cleared gpu not-ready taint", "node", n.Name, "allocatable", qty.String())
	}
}

// nodeAnchorAnnotation is the Karpenter-recognized Node-level annotation that
// excludes a node from voluntary disruption (consolidation, drift, expiration).
// Distinct from the same-named pod-level annotation, which only affects pods
// not classified as DaemonSet — slurmd is a DS, so the pod variant doesn't
// help us. Node-level is honored independently.
const nodeAnchorAnnotation = "karpenter.sh/do-not-disrupt"

// reconcileNodeAnchors brings each Karpenter worker node's
// `karpenter.sh/do-not-disrupt` annotation in line with central's
// ProtectedNodes set:
//
//	in protected, no annotation  → set annotation
//	in protected, has annotation → no-op
//	not protected, has annotation → remove annotation
//	not protected, no annotation  → no-op
//
// Idempotent. Single writer. Karpenter consolidates idle (un-annotated) nodes
// via the `WhenEmpty` policy after `consolidateAfter` (180s) once the cap-pod
// is gone — retaining capacity longer before teardown so burst jobs can reuse
// the warm node. The annotation is the only signal we need to manage.
func reconcileNodeAnchors(ctx context.Context, cs kubernetes.Interface, protectedNodes []string) {
	protected := make(map[string]struct{}, len(protectedNodes))
	for _, n := range protectedNodes {
		protected[n] = struct{}{}
	}

	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "karpenter.sh/nodepool",
	})
	if err != nil {
		slog.Warn("reconcile node anchors: list nodes failed", "err", err)
		return
	}

	for i := range list.Items {
		n := &list.Items[i]
		_, want := protected[n.Name]
		_, has := n.Annotations[nodeAnchorAnnotation]
		switch {
		case want && !has:
			patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:"true"}}}`, nodeAnchorAnnotation))
			if _, err := cs.CoreV1().Nodes().Patch(ctx, n.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
				slog.Warn("set node anchor failed", "node", n.Name, "err", err)
				continue
			}
			slog.Info("set node anchor", "node", n.Name)
		case !want && has:
			patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:null}}}`, nodeAnchorAnnotation))
			if _, err := cs.CoreV1().Nodes().Patch(ctx, n.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
				slog.Warn("clear node anchor failed", "node", n.Name, "err", err)
				continue
			}
			slog.Info("cleared node anchor", "node", n.Name)
		}
	}
}

// reconcileCapacityPods drives `existing → desired` per shape in BOTH
// directions. Symmetric: scale-up creates cap-pods, scale-down deletes them.
//
// Selection order when deleting surplus cap-pods (existing > desired):
//  1. Pods not yet bound to a node (Pending in K8s) — safest, no node side
//     effects.
//  2. Pods bound to non-protected nodes (Slurm IDLE) — deletion frees the
//     node for Karpenter's `WhenEmpty` consolidation.
//  3. Pods bound to protected nodes (Slurm has a job there). Last resort —
//     should be rare (only when desired drops faster than the protected set
//     refreshes). The Node-level `karpenter.sh/do-not-disrupt` annotation
//     prevents Karpenter from consolidating these nodes even after the cap-pod
//     is gone, so the running job survives until the next anchor reconcile
//     re-creates a cap-pod (if demand still requires it).
func reconcileCapacityPods(
	ctx context.Context,
	cs kubernetes.Interface,
	namespace string,
	desiredShapes []shapeCommand,
	protectedNodes []string,
	existingPods []corev1.Pod,
) (created, deleted int) {
	protected := make(map[string]struct{}, len(protectedNodes))
	for _, n := range protectedNodes {
		protected[n] = struct{}{}
	}

	// Bucket existing pods by shape for both halves of the reconcile.
	byShape := make(map[string][]corev1.Pod)
	for i := range existingPods {
		p := existingPods[i]
		shapeName := p.Labels[labelShape]
		if shapeName == "" {
			continue
		}
		byShape[shapeName] = append(byShape[shapeName], p)
	}

	// Index desired shapes for the scale-down sweep over orphaned shapes
	// (existing pods of a shape no longer present in desired = desired 0).
	desiredByName := make(map[string]shapeCommand, len(desiredShapes))
	for _, cmd := range desiredShapes {
		desiredByName[cmd.Name] = cmd
	}

	// ── Scale up + per-shape scale down ─────────────────────────────────
	for _, cmd := range desiredShapes {
		existing := int32(len(byShape[cmd.Name]))
		switch {
		case existing < cmd.Replicas:
			for i := existing; i < cmd.Replicas; i++ {
				pod, err := buildCapacityPod(namespace, cmd)
				if err != nil {
					slog.Error("build capacity pod failed", "shape", cmd.Name, "err", err)
					break
				}
				if _, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
					slog.Error("create capacity pod failed", "shape", cmd.Name, "err", err)
					break
				}
				slog.Info("created capacity pod", "pod", pod.Name, "shape", cmd.Name)
				created++
			}
		case existing > cmd.Replicas:
			surplus := int(existing - cmd.Replicas)
			deleted += deleteSurplusCapPods(ctx, cs, namespace, byShape[cmd.Name], surplus, protected, cmd.Name)
		}
	}

	// ── Drop fully-orphaned shapes (desired = 0) ────────────────────────
	for shapeName, pods := range byShape {
		if _, stillDesired := desiredByName[shapeName]; stillDesired {
			continue
		}
		deleted += deleteSurplusCapPods(ctx, cs, namespace, pods, len(pods), protected, shapeName)
	}

	return created, deleted
}

// deleteSurplusCapPods removes `n` cap-pods from `pods`, ordered by safety:
// unbound first, then non-protected nodes, then protected nodes. Returns the
// number actually deleted (best-effort — a delete failure is logged and the
// loop continues with the next candidate).
func deleteSurplusCapPods(
	ctx context.Context,
	cs kubernetes.Interface,
	namespace string,
	pods []corev1.Pod,
	n int,
	protected map[string]struct{},
	shapeName string,
) int {
	if n <= 0 || len(pods) == 0 {
		return 0
	}

	var unbound, onIdle, onProtected []corev1.Pod
	for _, p := range pods {
		switch {
		case p.Spec.NodeName == "":
			unbound = append(unbound, p)
		default:
			if _, prot := protected[p.Spec.NodeName]; prot {
				onProtected = append(onProtected, p)
			} else {
				onIdle = append(onIdle, p)
			}
		}
	}

	ordered := make([]corev1.Pod, 0, len(pods))
	ordered = append(ordered, unbound...)
	ordered = append(ordered, onIdle...)
	ordered = append(ordered, onProtected...)

	deleted := 0
	for _, p := range ordered {
		if deleted >= n {
			break
		}
		if err := cs.CoreV1().Pods(namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			slog.Warn("delete surplus cap pod failed", "pod", p.Name, "shape", shapeName, "err", err)
			continue
		}
		slog.Info("deleted surplus cap pod", "pod", p.Name, "shape", shapeName, "node", p.Spec.NodeName)
		deleted++
	}
	return deleted
}

// deleteNodeClaim looks up the Karpenter NodeClaim owning the given K8s node
// (by NodeName match) and deletes it. Karpenter's termination finalizer takes
// over: drains pods, runs slurmd preStop (`scontrol delete node`), terminates
// the EC2 instance. Idempotent — deleting an already-terminating NodeClaim is
// a no-op.
//
// Only caller is the ghost detector (ghosts.go) — for nodes whose slurmd never
// registered, there is no graceful Slurm-side teardown to coordinate, so going
// directly through Karpenter is correct. The graceful scale-down path no
// longer needs this: Karpenter's `WhenEmpty` consolidation handles idle-node
// teardown once the cap-pod is gone and the Node anchor annotation is cleared.
func deleteNodeClaim(ctx context.Context, dyn dynamic.Interface, claims []nodeClaim, nodeName string) error {
	var claimName string
	for _, c := range claims {
		if c.NodeName == nodeName {
			claimName = c.Name
			break
		}
	}
	if claimName == "" {
		return fmt.Errorf("no NodeClaim found for node %q", nodeName)
	}
	err := dyn.Resource(nodeClaimGVR).Delete(ctx, claimName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rng.Intn(len(chars))]
	}
	return string(b)
}

// ── Slurm key sync helpers ──────────────────────────────────────────────────

func getSlurmKeyHash(ctx context.Context, cs kubernetes.Interface, namespace string) string {
	secret, err := cs.CoreV1().Secrets(namespace).Get(ctx, "slurm-secret", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	h := sha256.Sum256(secret.Data["slurm.key"])
	return hex.EncodeToString(h[:])
}

func applySlurmSecret(ctx context.Context, cs kubernetes.Interface, namespace string, keyData []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "slurm-secret", Namespace: namespace},
		Data:       map[string][]byte{"slurm.key": keyData},
	}
	_, err := cs.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	}
	return err
}

// ── Heartbeat ────────────────────────────────────────────────────────────────

func detectAgentIP() string {
	if v := os.Getenv("POD_IP"); v != "" {
		return v
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// listWorkerNodeNames returns the names of Karpenter-managed worker nodes.
// Filtered by the karpenter.sh/nodepool label so control-plane and other
// nodes don't pollute the orphan-detection signal central runs on this list.
func listWorkerNodeNames(ctx context.Context, cs kubernetes.Interface) ([]string, error) {
	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "karpenter.sh/nodepool",
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for _, n := range list.Items {
		names = append(names, n.Name)
	}
	return names, nil
}

func sendHeartbeat(ctx context.Context, cfg config, client *http.Client, capPods []corev1.Pod, k8sNodes []string, lastVersion int, slurmKeyHash string, claims []nodeClaim, prices []priceQuote) (*heartbeatResponse, error) {
	capCounts := countByShape(capPods)
	capInfo := make(map[string]capacityInfo, len(capCounts))
	for shape, count := range capCounts {
		capInfo[shape] = capacityInfo{Count: count}
	}

	body, _ := json.Marshal(heartbeatRequest{
		ClusterID:          cfg.clusterID,
		AgentVersion:       agentVersion,
		CapacityPods:       capInfo,
		LastAppliedVersion: lastVersion,
		SlurmKeyHash:       slurmKeyHash,
		AgentIP:            detectAgentIP(),
		K8sNodes:           k8sNodes,
		NodeClaims:         claims,
		Prices:             prices,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.centralAPIURL+"/v1/internal/agent/heartbeat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.agentAPIKey != "" {
		req.Header.Set("X-Agent-Key", cfg.agentAPIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("heartbeat returned %d", resp.StatusCode)
	}

	var hbResp heartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&hbResp); err != nil {
		return nil, err
	}
	return &hbResp, nil
}

// ── Main loop ────────────────────────────────────────────────────────────────

// keySyncInterval is retained as a structural marker: in the past it gated
// hash computation to every ~20 cycles. That caused central to see an empty
// `slurm_key_hash` on 19/20 heartbeats and mis-interpret it as "agent has
// no key" — logging a spurious "sending slurm key" every cycle and doing
// the key-apply work needlessly. sha256 of a 32-byte key is ~microseconds,
// so we now compute it every cycle and let central compare.
const keySyncInterval = 1

func run(ctx context.Context, cfg config) error {
	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(k8sCfg)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(k8sCfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	// Separate client for the in-cluster Karpenter metrics scrape — the
	// /metrics body can be several MiB on large fleets, so a tighter timeout
	// here keeps a stuck scrape from blocking the heartbeat.
	scrapeClient := &http.Client{Timeout: 5 * time.Second}
	lastAppliedVersion := 0
	keySyncCounter := 0

	slog.Info("edge agent started",
		"cluster", cfg.clusterID,
		"api", cfg.centralAPIURL,
		"poll", cfg.pollInterval,
		"version", agentVersion,
	)

	for {
		cycleStart := time.Now()
		keySyncCounter++

		if err := cycle(ctx, cfg, cs, dyn, httpClient, scrapeClient, &lastAppliedVersion, keySyncCounter); err != nil {
			slog.Warn("cycle error", "err", err)
		}

		elapsed := time.Since(cycleStart)
		remaining := cfg.pollInterval - elapsed
		if remaining > 0 {
			select {
			case <-time.After(remaining):
			case <-ctx.Done():
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

func cycle(ctx context.Context, cfg config, cs kubernetes.Interface, dyn dynamic.Interface, httpClient *http.Client, scrapeClient *http.Client, lastVersion *int, keySyncCounter int) error {
	capPods, err := listCapacityPods(ctx, cs, cfg.namespace)
	if err != nil {
		return fmt.Errorf("list capacity pods: %w", err)
	}
	slog.Debug("listed capacity pods", "count", len(capPods))

	// Stamp the GPU isolation taint on any new GPU node before we
	// reconcile cap-pods. Race-tolerant: a CPU cap-pod created this cycle
	// for a brand-new GPU node still can't bind because it lacks the
	// toleration; if we miss a cycle the taint just lands on the next tick.
	reconcileGPUTaints(ctx, cs)
	// Clear the `nvidia.com/gpu=not-ready` kubelet startup taint from any
	// GPU node where the device plugin has advertised nvidia.com/gpu as
	// Allocatable. Lets pending GPU cap-pods bind once the plugin is up.
	reconcileGPUReadyTaint(ctx, cs)

	k8sNodes, err := listWorkerNodeNames(ctx, cs)
	if err != nil {
		slog.Warn("list worker nodes failed", "err", err)
		// Send nil on list failure → serializes as JSON null → central skips
		// the orphan sweep this cycle. A successful empty list serializes
		// as `[]` and lets central sweep orphans even when no workers exist.
		k8sNodes = nil
	} else if k8sNodes == nil {
		// Defensive: guarantee empty non-nil on success so JSON is `[]`.
		k8sNodes = []string{}
	}

	// Karpenter NodeClaim list — central uses this to compute cluster burn.
	// nil on list error keeps central's previous snapshot (until 90s stale)
	// instead of zeroing burn from a transient list failure.
	claims := listNodeClaims(ctx, dyn)
	slog.Debug("listed nodeclaims", "count", len(claims))

	// Karpenter price metric scrape. nil/empty on scrape failure — central
	// keeps the previous price table.
	prices := scrapeKarpenterPrices(ctx, scrapeClient)
	slog.Debug("scraped karpenter prices", "count", len(prices))

	// Compute slurm key hash on throttled interval (every ~5 min)
	var slurmKeyHash string
	if keySyncCounter%keySyncInterval == 0 {
		slurmKeyHash = getSlurmKeyHash(ctx, cs, cfg.namespace)
		slog.Debug("slurm key hash computed", "hash", slurmKeyHash)
	}

	resp, err := sendHeartbeat(ctx, cfg, httpClient, capPods, k8sNodes, *lastVersion, slurmKeyHash, claims, prices)
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}

	// Detect central pod restart: version counter resets
	if resp.ScalingCommandsVersion > 0 && resp.ScalingCommandsVersion < *lastVersion {
		slog.Info("central version reset detected, resetting lastAppliedVersion",
			"central_version", resp.ScalingCommandsVersion,
			"local_version", *lastVersion,
		)
		*lastVersion = 0
	}

	// Reconcile Node anchors first so any node entering/leaving the
	// protected set has its annotation updated before cap-pod scale-down
	// runs. This minimizes the window where a cap-pod is deleted off a
	// node whose anchor annotation is briefly absent.
	reconcileNodeAnchors(ctx, cs, resp.ProtectedNodes)

	// Symmetric per-shape reconcile: drives existing → desired in both
	// directions every cycle. The version-gating from v1.1 is gone — a
	// version-only trigger left surplus cap-pods stranded after the first
	// cycle that observed `existing > desired`. Reconciler is cheap (a
	// pod list + a small loop), and idempotent.
	commands := resp.ScalingCommands
	if commands == nil {
		commands = []shapeCommand{}
	}
	created, deleted := reconcileCapacityPods(ctx, cs, cfg.namespace, commands, resp.ProtectedNodes, capPods)
	if resp.ScalingCommandsVersion > *lastVersion {
		*lastVersion = resp.ScalingCommandsVersion
	}
	if created > 0 || deleted > 0 {
		slog.Info("reconciled capacity pods",
			"version", resp.ScalingCommandsVersion,
			"created", created,
			"deleted", deleted,
			"protected_nodes", len(resp.ProtectedNodes),
		)
	}
	// Reclaim ghost K8s nodes — workers whose slurmd never registered with
	// slurmctld (image-pull failure, GPU misclassification, etc.). Catches
	// the symmetric counterpart to central's sweepOrphanSlurmNodes:
	// central catches Slurm-side ghosts; this catches K8s-side ghosts.
	reclaimGhostNodes(ctx, cs, dyn, claims, cfg.namespace, cfg.ghostTimeout)

	// Apply slurm key if central sent one (hash mismatch or first sync)
	if resp.SlurmKey != "" {
		keyData, err := base64.StdEncoding.DecodeString(resp.SlurmKey)
		if err != nil {
			slog.Warn("slurm key decode failed", "err", err)
		} else if err := applySlurmSecret(ctx, cs, cfg.namespace, keyData); err != nil {
			slog.Warn("slurm key apply failed", "err", err)
		} else {
			slog.Info("slurm key updated")
		}
	}

	return nil
}
