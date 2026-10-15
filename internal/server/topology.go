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
	"fmt"

	"k8s.io/klog/v2"

	"github.com/Project-HAMi/ascend-device-plugin/internal/monitor"
)

// superPodTopologyFunc resolves this node's position in its superpod. It is a
// variable so tests can stub the DCMI-backed probe.
var superPodTopologyFunc = monitor.GetSuperPodTopology

// topologyAbsentNotice announces that a node carries no superpod annotation.
// It is a variable so tests can observe the announcement without reading klog.
var topologyAbsentNotice = func(node, reason string) {
	klog.Infof("Node %s carries no superpod annotation: %s", node, reason)
}

// appendSuperpodAnnotations adds the topology annotations HAMi's scheduler
// reads to keep one inference service inside a single superpod or rack.
// Nodes without superpod semantics stay unannotated, and a driver that cannot
// answer must never hold up device registration.
func (ps *PluginServer) appendSuperpodAnnotations(annos map[string]string) {
	superPod, err := superPodTopologyFunc()
	if err != nil {
		ps.announceTopologyAbsent(fmt.Sprintf("the topology probe failed: %v", err))
		return
	}
	written := 0
	for key, value := range superPod.Annotations() {
		annos[key] = value
		written++
	}
	if written == 0 {
		ps.announceTopologyAbsent("the driver reports no super-pod identity")
		return
	}
	// A resolved identity re-arms the notice, so a domain lost again later is
	// still worth a line.
	ps.topologyAbsentAnnounced = false
	klog.V(4).Infof("Node %s superpod topology: superpodid=%d rackid=%d serverid=%d",
		ps.nodeName, superPod.SuperPodID, superPod.RackID, superPod.ServerID)
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
