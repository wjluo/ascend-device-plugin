package monitor

import (
	"math"
	"testing"

	"github.com/Project-HAMi/ascend-device-plugin/internal/topology"
)

// The driver answers with 0xffffffff for a level it does not have, and a value
// above MaxInt32 cannot survive the int32 the annotation carries. Both have to
// land on InvalidID: an id that silently wraps would name a superpod that does
// not exist, and the scheduler would converge workloads into it.
func TestNormalizeDriverID(t *testing.T) {
	tests := []struct {
		name string
		raw  uint32
		want int32
	}{
		{"zero is a real id, not a missing one", 0, 0},
		{"ordinary id", 7, 7},
		{"largest id that fits the annotation", math.MaxInt32, math.MaxInt32},
		{"one above the range", math.MaxInt32 + 1, topology.InvalidID},
		{"driver sentinel for no such level", unknownDriverID, topology.InvalidID},
		{"largest representable value", math.MaxUint32, topology.InvalidID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeDriverID(tc.raw); got != tc.want {
				t.Errorf("normalizeDriverID(%d) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// A negative normalised id would read as HAMi's "this level does not exist"
// sentinel, so the function must never return one other than InvalidID.
func TestNormalizeDriverIDNeverWrapsNegative(t *testing.T) {
	for raw := uint32(0); raw < 1024; raw++ {
		if got := normalizeDriverID(raw); got < topology.InvalidID {
			t.Fatalf("normalizeDriverID(%d) = %d", raw, got)
		}
	}
	for _, raw := range []uint32{math.MaxInt32, math.MaxInt32 + 1, unknownDriverID, math.MaxUint32} {
		if got := normalizeDriverID(raw); got < topology.InvalidID {
			t.Errorf("normalizeDriverID(%d) = %d, want at least %d", raw, got, topology.InvalidID)
		}
	}
}
