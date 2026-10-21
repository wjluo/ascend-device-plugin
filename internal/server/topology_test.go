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
	"errors"
	"reflect"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Project-HAMi/HAMi/pkg/util/client"
	"github.com/Project-HAMi/ascend-device-plugin/internal/topology"
)

func stubSuperPodProbe(t *testing.T, probe func() (topology.SuperPod, error)) {
	t.Helper()
	orig := superPodTopologyFunc
	superPodTopologyFunc = probe
	t.Cleanup(func() { superPodTopologyFunc = orig })
}

// registeredAnnotations runs the real registration path and returns what ended
// up on the Node, so the assertions cover the wiring, not just the renderer.
func registeredAnnotations(t *testing.T) map[string]string {
	t.Helper()
	return registeredAnnotationsOn(t, nil, false)
}

// registeredAnnotationsOn runs the real registration path against a node that
// already carries the given annotations, and returns what the node ends up with.
func registeredAnnotationsOn(t *testing.T, seed map[string]string, pruneStale bool) map[string]string {
	t.Helper()
	stored := make(map[string]string, len(seed))
	for key, value := range seed {
		stored[key] = value
	}
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "topo-node", Annotations: stored}}
	t.Cleanup(setupFakeClient(nil, []*v1.Node{node}))
	ps := &PluginServer{
		nodeName:           node.Name,
		registerAnno:       "hami.io/node-ascend-register",
		handshakeAnno:      "hami.io/node-ascend-handshake",
		mgr:                &FakeManager{},
		topologyPruneStale: pruneStale,
	}
	if err := ps.registerHAMi(); err != nil {
		t.Fatalf("registerHAMi() = %v", err)
	}
	updated, err := client.KubeClient.CoreV1().Nodes().Get(context.Background(), node.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	return updated.Annotations
}

func TestRegisterHAMiWritesSuperpodAnnotations(t *testing.T) {
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{SuperPodID: 3, RackID: 1, ServerID: 2, HasRackTier: true}, nil
	})

	annos := registeredAnnotations(t)
	want := map[string]string{
		topology.SuperPodIDAnnotation: "3",
		topology.RackIDAnnotation:     "1",
		topology.ServerIDAnnotation:   "2",
	}
	for key, value := range want {
		if annos[key] != value {
			t.Errorf("annotation %s = %q, want %q", key, annos[key], value)
		}
	}
}

// A3 has no rack level, so the rack key must stay absent; the scheduler reads
// its absence as a two-tier cluster.
func TestRegisterHAMiOmitsRackForTwoTierChip(t *testing.T) {
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{SuperPodID: 3, RackID: topology.InvalidID, ServerID: 2}, nil
	})

	annos := registeredAnnotations(t)
	if _, ok := annos[topology.RackIDAnnotation]; ok {
		t.Errorf("%s written for a chip without a rack level", topology.RackIDAnnotation)
	}
	if annos[topology.SuperPodIDAnnotation] != "3" {
		t.Errorf("superpod id = %q, want 3", annos[topology.SuperPodIDAnnotation])
	}
}

// Single-node NPUs are not in a superpod. Writing nothing keeps them out of
// every topology decision instead of parking them in "superpod -1".
func TestRegisterHAMiWithoutSuperpodIdentityStaysUnannotated(t *testing.T) {
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{SuperPodID: topology.InvalidID}, nil
	})

	annos := registeredAnnotations(t)
	for _, key := range []string{topology.SuperPodIDAnnotation, topology.RackIDAnnotation, topology.ServerIDAnnotation} {
		if _, ok := annos[key]; ok {
			t.Errorf("%s written on a node that is not in a superpod", key)
		}
	}
}

// Device registration must not fail because the topology probe did - on a
// driver without super-pod support every node would otherwise go unregistered.
func TestRegisterHAMiKeepsWorkingWhenProbeFails(t *testing.T) {
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{}, errors.New("dcmi init failed")
	})

	annos := registeredAnnotations(t)
	if annos["hami.io/node-ascend-register"] == "" {
		t.Error("device registration annotation missing after a failed topology probe")
	}
	if _, ok := annos[topology.SuperPodIDAnnotation]; ok {
		t.Error("superpod id written after a failed topology probe")
	}
}

// captureTopologyNotices replaces the announcement sink so the tests can see
// what the registration loop reported without reading klog's output.
func captureTopologyNotices(t *testing.T) *[]string {
	t.Helper()
	orig := topologyAbsentNotice
	notices := []string{}
	topologyAbsentNotice = func(node, reason string) { notices = append(notices, node+": "+reason) }
	t.Cleanup(func() { topologyAbsentNotice = orig })
	return &notices
}

// HAMi exempts an unannotated node from the superpod policy instead of placing
// it, so a workload that fails to converge because of this node has to be
// explainable from the plugin log. Registration repeats every 30s, so the
// reason is announced when the state changes and not on every round.
func TestAppendSuperpodAnnotationsAnnouncesMissingDomainOnce(t *testing.T) {
	ps := &PluginServer{nodeName: "n1"}
	notices := captureTopologyNotices(t)

	resolved := topology.SuperPod{SuperPodID: 3, ServerID: 1}
	unresolved := topology.SuperPod{SuperPodID: topology.InvalidID}
	probe := unresolved
	stubSuperPodProbe(t, func() (topology.SuperPod, error) { return probe, nil })

	withDriverReporting := func(want topology.SuperPod) map[string]string {
		t.Helper()
		probe = want
		annos := map[string]string{}
		ps.appendSuperpodAnnotations(annos, nil)
		return annos
	}

	annos := withDriverReporting(unresolved)
	if len(*notices) != 1 {
		t.Fatalf("notices = %q, want the missing domain announced once", *notices)
	}
	if !strings.Contains((*notices)[0], "super-pod") {
		t.Errorf("notice = %q, want it to name the missing super-pod identity", (*notices)[0])
	}
	if _, ok := annos[topology.SuperPodIDAnnotation]; ok {
		t.Error("superpod id written although the driver gave no identity")
	}

	withDriverReporting(unresolved)
	if len(*notices) != 1 {
		t.Fatalf("notices = %q, want silence while the state is unchanged", *notices)
	}

	if annos := withDriverReporting(resolved); annos[topology.SuperPodIDAnnotation] != "3" {
		t.Fatalf("superpod id = %q, want 3", annos[topology.SuperPodIDAnnotation])
	}
	if len(*notices) != 1 {
		t.Fatalf("notices = %q, want no announcement for a resolved topology", *notices)
	}

	withDriverReporting(unresolved)
	if len(*notices) != 2 {
		t.Fatalf("notices = %q, want a fresh one after the identity was lost again", *notices)
	}
}

// A probe that errors leaves the node unannotated for the same reason as an
// absent identity, but the operator needs the cause - so it is announced too.
func TestAppendSuperpodAnnotationsAnnouncesProbeFailure(t *testing.T) {
	ps := &PluginServer{nodeName: "n2"}
	notices := captureTopologyNotices(t)
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{}, errors.New("dcmi init failed")
	})

	annos := map[string]string{}
	ps.appendSuperpodAnnotations(annos, nil)

	if len(*notices) != 1 {
		t.Fatalf("notices = %q, want the failed probe announced", *notices)
	}
	if !strings.Contains((*notices)[0], "dcmi init failed") {
		t.Errorf("notice = %q, want it to carry the probe error", (*notices)[0])
	}
	if _, ok := annos[topology.SuperPodIDAnnotation]; ok {
		t.Error("superpod id written after a failed probe")
	}
}

// captureTopologyRemovals replaces the patch that takes annotations off the
// Node, so a test can see what the plugin decided to remove without a cluster.
func captureTopologyRemovals(t *testing.T) *[][]string {
	t.Helper()
	orig := removeTopologyAnnotations
	removals := [][]string{}
	removeTopologyAnnotations = func(node string, keys []string) error {
		removals = append(removals, keys)
		return nil
	}
	t.Cleanup(func() { removeTopologyAnnotations = orig })
	return &removals
}

// Registration only ever merges, so an annotation the node has outgrown stays
// on it forever: HAMi then keeps steering a workload into a rack the server no
// longer belongs to. Removing the levels the new topology does not justify is
// what makes a moved or re-cabled node converge on its new domain.
func TestAppendSuperpodAnnotationsPrunesLostLevels(t *testing.T) {
	ps := &PluginServer{nodeName: "n3", topologyPruneStale: true}
	removals := captureTopologyRemovals(t)
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{SuperPodID: 3, ServerID: 2}, nil
	})

	current := map[string]string{
		topology.SuperPodIDAnnotation: "3",
		topology.RackIDAnnotation:     "1",
		topology.ServerIDAnnotation:   "2",
	}
	annos := map[string]string{}
	ps.appendSuperpodAnnotations(annos, current)

	if len(*removals) != 1 || !reflect.DeepEqual((*removals)[0], []string{topology.RackIDAnnotation}) {
		t.Fatalf("removals = %v, want the rack id the node no longer reports", *removals)
	}
	if annos[topology.ServerIDAnnotation] != "2" {
		t.Errorf("server id = %q, want it still written in the same round", annos[topology.ServerIDAnnotation])
	}
}

// Losing the super-pod identity takes the whole domain with it: a node that is
// no longer in a superpod must not keep advertising which one it was in.
func TestAppendSuperpodAnnotationsPrunesEverythingWithoutIdentity(t *testing.T) {
	ps := &PluginServer{nodeName: "n4", topologyPruneStale: true}
	removals := captureTopologyRemovals(t)
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{SuperPodID: topology.InvalidID}, nil
	})

	ps.appendSuperpodAnnotations(map[string]string{}, map[string]string{
		topology.SuperPodIDAnnotation: "3",
		topology.RackIDAnnotation:     "1",
	})

	want := []string{topology.SuperPodIDAnnotation, topology.RackIDAnnotation}
	if len(*removals) != 1 || !reflect.DeepEqual((*removals)[0], want) {
		t.Fatalf("removals = %v, want %v", *removals, want)
	}
}

// A failed probe says nothing about the node's topology, so it must not cost
// the node the domain it reported while the driver was reachable. Losing it
// would make the node look topology-less and exempt it from convergence.
func TestAppendSuperpodAnnotationsKeepsAnnotationsWhenProbeFails(t *testing.T) {
	ps := &PluginServer{nodeName: "n5", topologyPruneStale: true}
	removals := captureTopologyRemovals(t)
	captureTopologyNotices(t)
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{}, errors.New("dcmi unreachable")
	})

	ps.appendSuperpodAnnotations(map[string]string{}, map[string]string{
		topology.SuperPodIDAnnotation: "3",
		topology.RackIDAnnotation:     "1",
	})

	if len(*removals) != 0 {
		t.Fatalf("removals = %v, want none after a probe that proved nothing", *removals)
	}
}

// Removal is off unless the operator asks for it: a team that sets
// hami.io/superpodid by hand would otherwise watch the plugin delete those
// values as soon as the driver disagrees with them.
func TestAppendSuperpodAnnotationsPruningIsOptIn(t *testing.T) {
	ps := &PluginServer{nodeName: "n6"}
	removals := captureTopologyRemovals(t)
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{SuperPodID: 3}, nil
	})

	ps.appendSuperpodAnnotations(map[string]string{}, map[string]string{
		topology.SuperPodIDAnnotation: "3",
		topology.RackIDAnnotation:     "1",
	})

	if len(*removals) != 0 {
		t.Fatalf("removals = %v, want none without the opt-in", *removals)
	}
}

// The removal has to reach the API server, not just the decision: a merge patch
// carries null for each key so the key is deleted rather than blanked.
// The decision only matters if it reaches the Node through the real
// registration path, merge patch and all.
func TestRegisterHAMiPrunesStaleAnnotationsThroughTheRealPath(t *testing.T) {
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{SuperPodID: 4, ServerID: 2}, nil
	})

	annos := registeredAnnotationsOn(t, map[string]string{
		topology.SuperPodIDAnnotation: "3",
		topology.RackIDAnnotation:     "1",
		topology.ServerIDAnnotation:   "9",
	}, true)

	if _, ok := annos[topology.RackIDAnnotation]; ok {
		t.Errorf("annotations = %v, want the rack id of the old domain removed", annos)
	}
	if annos[topology.SuperPodIDAnnotation] != "4" {
		t.Errorf("superpod id = %q, want the new domain written in the same round", annos[topology.SuperPodIDAnnotation])
	}
	if annos[topology.ServerIDAnnotation] != "2" {
		t.Errorf("server id = %q, want 2", annos[topology.ServerIDAnnotation])
	}
}

func TestRegisterHAMiKeepsStaleAnnotationsUnlessPruningIsOn(t *testing.T) {
	stubSuperPodProbe(t, func() (topology.SuperPod, error) {
		return topology.SuperPod{SuperPodID: 4, ServerID: 2}, nil
	})

	annos := registeredAnnotationsOn(t, map[string]string{
		topology.RackIDAnnotation: "1",
	}, false)

	if annos[topology.RackIDAnnotation] != "1" {
		t.Errorf("annotations = %v, want the rack id kept without the opt-in", annos)
	}
}

func TestRemoveTopologyAnnotationsPatchesKeysToNull(t *testing.T) {
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n7", Annotations: map[string]string{
		topology.SuperPodIDAnnotation: "3",
		topology.RackIDAnnotation:     "1",
		topology.ServerIDAnnotation:   "2",
	}}}
	t.Cleanup(setupFakeClient(nil, []*v1.Node{node}))

	if err := removeTopologyAnnotations(node.Name, []string{topology.RackIDAnnotation}); err != nil {
		t.Fatalf("removeTopologyAnnotations() = %v", err)
	}
	updated, err := client.KubeClient.CoreV1().Nodes().Get(context.Background(), node.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if _, ok := updated.Annotations[topology.RackIDAnnotation]; ok {
		t.Errorf("annotations = %v, want the rack id removed", updated.Annotations)
	}
	for key, value := range map[string]string{
		topology.SuperPodIDAnnotation: "3",
		topology.ServerIDAnnotation:   "2",
	} {
		if updated.Annotations[key] != value {
			t.Errorf("annotation %s = %q, want %q left untouched", key, updated.Annotations[key], value)
		}
	}
}
