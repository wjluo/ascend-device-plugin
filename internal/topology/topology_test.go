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

package topology

import (
	"reflect"
	"testing"
)

func TestSuperPodAnnotationsThreeTier(t *testing.T) {
	sp := SuperPod{SuperPodID: 1, RackID: 2, ServerID: 3, HasRackTier: true}
	want := map[string]string{
		SuperPodIDAnnotation: "1",
		RackIDAnnotation:     "2",
		ServerIDAnnotation:   "3",
	}
	if got := sp.Annotations(); !reflect.DeepEqual(got, want) {
		t.Errorf("Annotations() = %v, want %v", got, want)
	}
}

// A3 has no rack level: reporting one would make the scheduler treat an
// arbitrary grouping as a convergence domain.
func TestSuperPodAnnotationsTwoTierOmitsRack(t *testing.T) {
	sp := SuperPod{SuperPodID: 7, RackID: 0, ServerID: 4}
	want := map[string]string{
		SuperPodIDAnnotation: "7",
		ServerIDAnnotation:   "4",
	}
	if got := sp.Annotations(); !reflect.DeepEqual(got, want) {
		t.Errorf("Annotations() = %v, want %v", got, want)
	}
}

// A node whose driver reports the "unknown" sentinel is not part of a
// superpod, so it must stay unannotated rather than claiming superpod 0.
func TestSuperPodAnnotationsWithoutSuperPodID(t *testing.T) {
	for _, id := range []int32{InvalidID, -2, -5} {
		sp := SuperPod{SuperPodID: id, RackID: 1, ServerID: 1, HasRackTier: true}
		if got := sp.Annotations(); got != nil {
			t.Errorf("SuperPodID %d: Annotations() = %v, want nil", id, got)
		}
	}
}

// A rack or server id the driver could not resolve must be left out; the
// scheduler then degrades to two-tier convergence instead of grouping every
// node into a phantom rack 0.
func TestSuperPodAnnotationsSkipsUnknownInnerLevels(t *testing.T) {
	sp := SuperPod{SuperPodID: 1, RackID: InvalidID, ServerID: -2, HasRackTier: true}
	want := map[string]string{SuperPodIDAnnotation: "1"}
	if got := sp.Annotations(); !reflect.DeepEqual(got, want) {
		t.Errorf("Annotations() = %v, want %v", got, want)
	}
}

func TestSuperPodUsable(t *testing.T) {
	if !(SuperPod{SuperPodID: 0}).Usable() {
		t.Error("superpod 0 is a valid identity, not a missing one")
	}
	if (SuperPod{SuperPodID: InvalidID}).Usable() {
		t.Error("an invalid superpod id must not be usable")
	}
}

// These keys are the contract with HAMi: its scheduler reads the same
// strings, declared independently in HAMi's pkg/util/types.go. Keep the two
// in sync when changing a value.
func TestAnnotationKeysMatchTheHamiContract(t *testing.T) {
	want := map[string]string{
		SuperPodIDAnnotation: "hami.io/superpodid",
		RackIDAnnotation:     "hami.io/rackid",
		ServerIDAnnotation:   "hami.io/serverid",
	}
	for got, expected := range want {
		if got != expected {
			t.Errorf("annotation key %q drifted from the HAMi contract %q", got, expected)
		}
	}
}

// RackTierForProduct decides whether a chip exposes the middle level. Only A5
// reports a rack id (Huawei's own plugin gates the same way); A3 and the
// single-node cards converge on the superpod alone.
func TestRackTierForProduct(t *testing.T) {
	tests := []struct {
		product string
		want    bool
	}{
		{"Ascend910A5", true},
		{"Ascend910A3", false},
		{"Ascend910B3", false},
		{"Ascend310B", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := RackTierForProduct(tc.product); got != tc.want {
			t.Errorf("RackTierForProduct(%q) = %v, want %v", tc.product, got, tc.want)
		}
	}
}
