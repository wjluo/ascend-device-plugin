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
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "topo-node", Annotations: map[string]string{}}}
	t.Cleanup(setupFakeClient(nil, []*v1.Node{node}))
	ps := &PluginServer{
		nodeName:      node.Name,
		registerAnno:  "hami.io/node-ascend-register",
		handshakeAnno: "hami.io/node-ascend-handshake",
		mgr:           &FakeManager{},
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
		ps.appendSuperpodAnnotations(annos)
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
	ps.appendSuperpodAnnotations(annos)

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
