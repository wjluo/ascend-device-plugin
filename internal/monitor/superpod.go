package monitor

import (
	"errors"
	"fmt"
	"math"

	"github.com/Project-HAMi/ascend-device-plugin/internal/topology"
)

// unknownDriverID is what the driver reports for a topology field it does not
// have (0xffffffff). MindCluster maps it to -1, and so do we.
const unknownDriverID = uint32(0xFFFFFFFF)

// ErrSuperPodUnsupported means this node's hardware or driver does not expose
// super-pod identity, for example an Ascend910B single-node system. Callers
// must treat it as "no topology to report", never as a registration failure.
var ErrSuperPodUnsupported = errors.New("superpod info not supported on this hardware/driver")

// GetSuperPodTopology returns where this node sits in its superpod. All chips
// of one node share the same identity, so the first device answers for the
// whole node.
func GetSuperPodTopology() (topology.SuperPod, error) {
	mgr, err := getDcManager()
	if err != nil {
		return topology.SuperPod{}, fmt.Errorf("%w: dcmi init: %v", ErrSuperPodUnsupported, err)
	}
	_, ids, err := mgr.DcGetLogicIDList()
	if err != nil {
		return topology.SuperPod{}, fmt.Errorf("%w: get logic ids: %v", ErrSuperPodUnsupported, err)
	}
	if len(ids) == 0 {
		return topology.SuperPod{}, fmt.Errorf("%w: no devices enumerated", ErrSuperPodUnsupported)
	}
	cardID, deviceID, err := mgr.DcGetCardIDDeviceID(ids[0])
	if err != nil {
		return topology.SuperPod{}, fmt.Errorf("%w: get card/device id: %v", ErrSuperPodUnsupported, err)
	}
	info, err := mgr.DcGetSuperPodInfo(cardID, deviceID)
	if err != nil {
		return topology.SuperPod{}, fmt.Errorf("%w: get superpod info: %v", ErrSuperPodUnsupported, err)
	}
	// The board name only decides whether a rack level exists. If it cannot be
	// read the node still reports its superpod, just without a rack.
	product, err := mgr.DcGetProductType(cardID, deviceID)
	if err != nil {
		product = ""
	}
	return topology.SuperPod{
		SuperPodID:  normalizeDriverID(info.SuperPodId),
		RackID:      normalizeDriverID(info.RackId),
		ServerID:    normalizeDriverID(info.ServerId),
		HasRackTier: topology.RackTierForProduct(product),
	}, nil
}

func normalizeDriverID(raw uint32) int32 {
	if raw == unknownDriverID || raw > math.MaxInt32 {
		return topology.InvalidID
	}
	return int32(raw)
}
