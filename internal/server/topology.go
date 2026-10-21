/*
 * Copyright 2026 The HAMi Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package server

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/Project-HAMi/HAMi/pkg/util/client"
	"github.com/Project-HAMi/ascend-device-plugin/internal/monitor"
	"github.com/Project-HAMi/ascend-device-plugin/internal/topology"
)

// superPodTopologyFunc resolves this node's position in its superpod. It is a
// variable so tests can stub the DCMI-backed probe.
var superPodTopologyFunc = monitor.GetSuperPodTopology

// topologyAbsentNotice announces that a node carries no superpod annotation.
// It is a variable so tests can observe the announcement without reading klog.
var topologyAbsentNotice = func(node, reason string) {
	klog.Infof("Node %s carries no superpod annotation: %s", node, reason)
}

// removeTopologyAnnotations deletes the listed annotations from the node. It is
// a variable so tests can observe the decision without an API server.
var removeTopologyAnnotations = func(node string, keys []string) error {
	if client.KubeClient == nil {
		return fmt.Errorf("kubernetes client is not initialized")
	}
	annotations := make(map[string]any, len(keys))
	for _, key := range keys {
		annotations[key] = nil
	}
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
	if err != nil {
		return err
	}
	_, err = client.KubeClient.CoreV1().Nodes().
		Patch(context.TODO(), node, k8stypes.MergePatchType, body, metav1.PatchOptions{})
	return err
}

// appendSuperpodAnnotations adds the topology annotations HAMi's scheduler
// reads to keep one inference service inside a single superpod or rack.
// Nodes without superpod semantics stay unannotated, and a driver that cannot
// answer must never hold up device registration.
//
// current is what the Node carries now, and is only used to decide which
// topology annotations the node has outgrown; pass nil where the caller has no
// node object to offer.
func (ps *PluginServer) appendSuperpodAnnotations(annos map[string]string, current map[string]string) {
	superPod, err := superPodTopologyFunc()
	if err != nil {
		// The probe proved nothing, so nothing may be taken away either.
		ps.announceTopologyAbsent(fmt.Sprintf("the topology probe failed: %v", err))
		return
	}
	desired := superPod.Annotations()
	written := 0
	for key, value := range desired {
		annos[key] = value
		written++
	}
	if written == 0 {
		ps.announceTopologyAbsent("the driver reports no super-pod identity")
		ps.pruneStaleTopologyAnnotations(current, nil)
		return
	}
	// A resolved identity re-arms the notice, so a domain lost again later is
	// still worth a line.
	ps.topologyAbsentAnnounced = false
	ps.pruneStaleTopologyAnnotations(current, desired)
	klog.V(4).Infof("Node %s superpod topology: superpodid=%d rackid=%d serverid=%d",
		ps.nodeName, superPod.SuperPodID, superPod.RackID, superPod.ServerID)
}

// pruneStaleTopologyAnnotations deletes the topology annotations the node
// carries but no longer reports. Registration only ever merges, so without this
// a server moved to another rack - or out of the superpod entirely - keeps
// advertising its old domain, and HAMi keeps converging workloads into it.
//
// Off unless the operator turns it on, because a hami.io/superpodid set by hand
// is indistinguishable from one this plugin wrote, and deleting an operator's
// value unasked is not this plugin's call.
func (ps *PluginServer) pruneStaleTopologyAnnotations(current, desired map[string]string) {
	if !ps.topologyPruneStale {
		return
	}
	stale := topology.StaleAnnotations(current, desired)
	if len(stale) == 0 {
		return
	}
	if err := removeTopologyAnnotations(ps.nodeName, stale); err != nil {
		klog.Errorf("Node %s could not remove stale superpod annotations %v: %v", ps.nodeName, stale, err)
		return
	}
	klog.Infof("Node %s no longer reports %v, removing those superpod annotations", ps.nodeName, stale)
}

// announceTopologyAbsent reports once per change of state, not once per round:
// registration repeats every 30s. The distinction matters because HAMi's
// superpod policy exempts an unannotated node rather than placing it, so a
// workload that fails to converge here otherwise looks like a scheduler
// decision instead of a node that never reported its domain.
//
// Written only from the registration loop, like lastPublishedDevices.
func (ps *PluginServer) announceTopologyAbsent(reason string) {
	if ps.topologyAbsentAnnounced {
		return
	}
	ps.topologyAbsentAnnounced = true
	topologyAbsentNotice(ps.nodeName, reason)
}
