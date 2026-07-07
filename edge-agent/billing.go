package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// nodeClaim mirrors central's billing.NodeClaim wire format. Field tags must
// stay in sync — the heartbeat payload is JSON-encoded with these names.
type nodeClaim struct {
	Name         string    `json:"name"`
	InstanceID   string    `json:"instance_id"`
	InstanceType string    `json:"instance_type"`
	Lifecycle    string    `json:"lifecycle"`
	Zone         string    `json:"zone"`
	NodePool     string    `json:"nodepool"`
	NodeName     string    `json:"node_name,omitempty"`
	BillableFrom time.Time `json:"billable_from"`
	Launched     bool      `json:"launched"`
}

// priceQuote is one row of Karpenter's price table — what the customer cluster
// would pay for that (instance_type, capacity_type, zone) right now.
type priceQuote struct {
	InstanceType string  `json:"instance_type"`
	Lifecycle    string  `json:"lifecycle"`
	Zone         string  `json:"zone"`
	USDPerHour   float64 `json:"usd_per_hour"`
}

var nodeClaimGVR = schema.GroupVersionResource{
	Group:    "karpenter.sh",
	Version:  "v1",
	Resource: "nodeclaims",
}

// listNodeClaims fetches all Karpenter NodeClaims from the local cluster.
// Returns nil on list error so the heartbeat encodes `null` (central skips
// the burn calc); empty slice means "agent listed and there are zero
// NodeClaims" (central treats burn as 0).
func listNodeClaims(ctx context.Context, dyn dynamic.Interface) []nodeClaim {
	list, err := dyn.Resource(nodeClaimGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	out := make([]nodeClaim, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		labels := item.GetLabels()
		nc := nodeClaim{
			Name:         item.GetName(),
			InstanceType: labels["node.kubernetes.io/instance-type"],
			Zone:         labels["topology.kubernetes.io/zone"],
			NodePool:     labels["karpenter.sh/nodepool"],
			Lifecycle:    capacityTypeToLifecycle(labels["karpenter.sh/capacity-type"]),
		}

		// Karpenter v1 moved providerID from spec to status (was spec.providerID
		// in v1beta1). Reading the wrong field made every NodeClaim look warming
		// because InstanceID came back empty and the central join with Slurm's
		// InstanceId never matched — UI showed "N nodes (N warming)" indefinitely.
		providerID, _, _ := unstructuredString(item.Object, "status", "providerID")
		nc.InstanceID = parseProviderID(providerID)

		nodeName, _, _ := unstructuredString(item.Object, "status", "nodeName")
		nc.NodeName = nodeName

		launched, launchedAt := launchedCondition(item.Object)
		nc.Launched = launched
		switch {
		case !launchedAt.IsZero():
			nc.BillableFrom = launchedAt
		default:
			nc.BillableFrom = item.GetCreationTimestamp().Time
		}

		out = append(out, nc)
	}
	return out
}

// parseProviderID extracts the EC2 instance ID from a Karpenter providerID
// of the form "aws:///us-east-1a/i-0abc123def456". Returns "" on any
// deviation from that exact shape.
func parseProviderID(providerID string) string {
	idx := strings.LastIndex(providerID, "/")
	if idx < 0 || idx == len(providerID)-1 {
		return ""
	}
	id := providerID[idx+1:]
	if !strings.HasPrefix(id, "i-") {
		return ""
	}
	return id
}

// capacityTypeToLifecycle normalizes Karpenter's capacity-type label onto
// the wire format central expects ("spot" or "on-demand").
func capacityTypeToLifecycle(ct string) string {
	switch ct {
	case "spot":
		return "spot"
	case "on-demand", "":
		return "on-demand"
	}
	return "on-demand"
}

func launchedCondition(obj map[string]interface{}) (bool, time.Time) {
	conds, _, _ := unstructuredSlice(obj, "status", "conditions")
	for _, raw := range conds {
		c, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := c["type"].(string)
		if t != "Launched" {
			continue
		}
		status, _ := c["status"].(string)
		if status != "True" {
			return false, time.Time{}
		}
		ltt, _ := c["lastTransitionTime"].(string)
		if ltt == "" {
			return true, time.Time{}
		}
		parsed, err := time.Parse(time.RFC3339, ltt)
		if err != nil {
			return true, time.Time{}
		}
		return true, parsed
	}
	return false, time.Time{}
}

func unstructuredString(obj map[string]interface{}, path ...string) (string, bool, bool) {
	cur := interface{}(obj)
	for _, key := range path {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "", false, false
		}
		v, present := m[key]
		if !present {
			return "", false, false
		}
		cur = v
	}
	s, ok := cur.(string)
	return s, true, ok
}

func unstructuredSlice(obj map[string]interface{}, path ...string) ([]interface{}, bool, bool) {
	cur := interface{}(obj)
	for _, key := range path {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false, false
		}
		v, present := m[key]
		if !present {
			return nil, false, false
		}
		cur = v
	}
	s, ok := cur.([]interface{})
	return s, true, ok
}

// karpenterMetricsURL is the in-cluster URL for Karpenter's Prometheus
// metrics endpoint. The Service is created by Karpenter's standard install.
const karpenterMetricsURL = "http://karpenter.karpenter.svc.cluster.local:8080/metrics"

// scrapeKarpenterPrices fetches Karpenter's offering price metric and parses
// each line into a priceQuote. Returns nil on any error — central treats nil
// as "agent couldn't scrape" and keeps the previous snapshot until it goes
// stale.
//
// Metric line shape:
//
//	karpenter_cloudprovider_instance_type_offering_price_estimate{capacity_type="spot",instance_type="i3.large",zone="us-east-1c"} 0.0417
//
// We only keep capacity_type ∈ {spot, on-demand}; "reserved" rows always
// report 0 in current Karpenter and would just pollute the table.
func scrapeKarpenterPrices(ctx context.Context, client *http.Client) []priceQuote {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, karpenterMetricsURL, nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	return parseKarpenterPriceMetrics(resp.Body)
}

func parseKarpenterPriceMetrics(r io.Reader) []priceQuote {
	const metricName = "karpenter_cloudprovider_instance_type_offering_price_estimate"
	out := make([]priceQuote, 0, 8192)

	sc := bufio.NewScanner(r)
	// Lines can be long when Karpenter publishes thousands of (type, AZ) rows.
	// 1 MiB is plenty for one line; the file itself is multi-MB but we read
	// line-by-line.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, metricName) {
			continue
		}
		// "name{labels} value"
		brace := strings.IndexByte(line, '{')
		closeBrace := strings.IndexByte(line, '}')
		if brace < 0 || closeBrace < brace {
			continue
		}
		labels := line[brace+1 : closeBrace]
		valueStr := strings.TrimSpace(line[closeBrace+1:])
		// Some Prom exporters include a trailing timestamp; take the first field.
		if sp := strings.IndexByte(valueStr, ' '); sp >= 0 {
			valueStr = valueStr[:sp]
		}
		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil || value <= 0 {
			continue
		}

		var capType, instType, zone string
		for _, kv := range splitLabels(labels) {
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			k := kv[:eq]
			v := strings.Trim(kv[eq+1:], `"`)
			switch k {
			case "capacity_type":
				capType = v
			case "instance_type":
				instType = v
			case "zone":
				zone = v
			}
		}
		if capType != "spot" && capType != "on-demand" {
			continue
		}
		if instType == "" || zone == "" {
			continue
		}
		out = append(out, priceQuote{
			InstanceType: instType,
			Lifecycle:    capType,
			Zone:         zone,
			USDPerHour:   value,
		})
	}
	return out
}

// splitLabels splits a Prometheus label list, respecting double-quoted
// values. Karpenter labels never contain commas inside values, but be
// safe.
func splitLabels(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
