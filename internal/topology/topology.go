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

// Package topology renders the super-pod identity reported by the NPU driver
// into the node annotations HAMi's scheduler reads to converge one inference
// service into a single superpod or rack.
//
// It has no driver dependency: callers probe the hardware and hand the result
// to SuperPod, which keeps the reporting rules unit-testable on their own.
package topology

import "strconv"

// Annotation keys written on the Node. They are the contract with HAMi, whose
// scheduler declares the same strings independently in pkg/util/types.go;
// changing a value here means changing it there.
const (
	SuperPodIDAnnotation = "hami.io/superpodid"
	RackIDAnnotation     = "hami.io/rackid"
	ServerIDAnnotation   = "hami.io/serverid"
)

// InvalidID is the value used for an id the driver could not supply. It
// matches the mapping HAMi's own plugins apply to the driver's 0xffffffff
// sentinel, and keeps the corresponding annotation off the node.
const InvalidID int32 = -1

// ascend910A5 is the only Ascend card that exposes a rack level today.
const ascend910A5 = "Ascend910A5"

// SuperPod is one node's position in the superpod topology. ids are
// non-negative when the driver supplied one, otherwise InvalidID.
type SuperPod struct {
	SuperPodID  int32
	RackID      int32
	ServerID    int32
	HasRackTier bool
}

// RackTierForProduct reports whether this card type has a rack between the
// superpod and the server. A3 stops at the superpod, so writing a rack id for
// it would invent a convergence domain the hardware does not have.
func RackTierForProduct(product string) bool {
	return product == ascend910A5
}

// AnnotationKeys lists every key Annotations can produce. A caller that has to
// clean up needs the complete set: the level a node has just lost is precisely
// the one missing from the freshly built map, so it cannot be found by
// diffing against the new value alone.
var AnnotationKeys = []string{SuperPodIDAnnotation, RackIDAnnotation, ServerIDAnnotation}

// StaleAnnotations returns the keys currently on the node that the reported
// topology no longer justifies, in AnnotationKeys order.
//
// An empty desired map means the node has no super-pod identity at all, so all
// three keys are stale. Callers must only ask this question when the hardware
// gave a definite answer: after a probe failure the absence of a value says
// nothing about the node, and removing the annotations would silently widen
// what HAMi's scheduler considers a topology-less cluster.
func StaleAnnotations(current, desired map[string]string) []string {
	var stale []string
	for _, key := range AnnotationKeys {
		if _, ok := current[key]; !ok {
			continue
		}
		if _, ok := desired[key]; !ok {
			stale = append(stale, key)
		}
	}
	return stale
}

// Usable reports whether the node belongs to a superpod at all.
func (sp SuperPod) Usable() bool {
	return sp.SuperPodID >= 0
}

// Annotations returns the node annotations for this topology, or nil when the
// node has no super-pod identity. Absent levels are omitted rather than
// written as a negative number.
//
// HAMi's superpod scheduling policy reads that omission as "this node's domain
// is unknown" and exempts it from being gated out, not as "this node is in
// another superpod". Writing a placeholder would make a node whose driver is
// not ready unschedulable for a service that already converged elsewhere.
func (sp SuperPod) Annotations() map[string]string {
	if !sp.Usable() {
		return nil
	}
	annos := map[string]string{
		SuperPodIDAnnotation: strconv.FormatInt(int64(sp.SuperPodID), 10),
	}
	if sp.HasRackTier && sp.RackID >= 0 {
		annos[RackIDAnnotation] = strconv.FormatInt(int64(sp.RackID), 10)
	}
	if sp.ServerID >= 0 {
		annos[ServerIDAnnotation] = strconv.FormatInt(int64(sp.ServerID), 10)
	}
	return annos
}
