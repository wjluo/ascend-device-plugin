package monitor

import (
	"ascend-common/common-utils/hwlog"
	"ascend-common/devmanager/common"
	"ascend-common/devmanager/dcmi"
	"context"
	"errors"
	"fmt"
	"sync"
)

type DeviceStat struct {
	Index       int
	UUID        string
	DeviceType  string
	MemoryUsed  uint64
	MemoryTotal uint64
	AICorePct   uint32
}

var (
	dcMgr     *dcmi.DcManager
	dcMgrOnce sync.Once
	dcMgrErr  error
)

func getDcManager() (*dcmi.DcManager, error) {
	dcMgrOnce.Do(func() {
		if err := hwlog.InitRunLogger(&hwlog.LogConfig{
			OnlyToStdout: true,
			LogLevel:     1, // warning
		}, context.Background()); err != nil {
			dcMgrErr = fmt.Errorf("hwlog init: %w", err)
			return
		}

		mgr := &dcmi.DcManager{}
		if err := mgr.DcInit(); err != nil {
			dcMgrErr = fmt.Errorf("dcmi init: %w", err)
			return
		}
		dcMgr = mgr
	})
	return dcMgr, dcMgrErr
}

func collectHostDeviceStats() ([]DeviceStat, error) {
	mgr, err := getDcManager()
	if err != nil {
		return nil, err
	}

	_, ids, err := mgr.DcGetLogicIDList()
	if err != nil {
		return nil, fmt.Errorf("get logic ids: %w", err)
	}

	var devices []DeviceStat
	for _, logicID := range ids {
		cardID, deviceID, err := mgr.DcGetCardIDDeviceID(logicID)
		if err != nil {
			continue
		}

		uuid, _ := mgr.DcGetDieID(cardID, deviceID, dcmi.VDIE)

		pt, _ := mgr.DcGetProductType(cardID, deviceID)

		memTotal := uint64(0)
		memUsed := uint64(0)
		if memInfo, err := mgr.DcGetMemoryInfo(cardID, deviceID); err == nil {
			memTotal = memInfo.MemorySize
			memUsed = memInfo.MemorySize - memInfo.MemoryAvailable
		}

		aicorePct := uint32(0)
		if rate, err := mgr.DcGetDeviceUtilizationRate(cardID, deviceID, common.AICore); err == nil {
			aicorePct = uint32(rate)
		}

		devices = append(devices, DeviceStat{
			Index:       int(logicID),
			UUID:        uuid,
			DeviceType:  pt,
			MemoryTotal: memTotal,
			MemoryUsed:  memUsed,
			AICorePct:   aicorePct,
		})
	}
	return devices, nil
}

// ErrSuperPodUnsupported reports that this node's hardware or driver does not
// expose super-pod identity (e.g. Ascend910B single-node systems). Callers
// must treat it as "no supernode affinity" instead of a scheduling failure.
var ErrSuperPodUnsupported = errors.New("superpod info not supported on this hardware/driver")

// unknownSuperPodID is the driver-side sentinel for "no super-pod info"
// (0xffffffff), mapped to -1 following the MindCluster clusterd convention.
const unknownSuperPodID = uint32(0xFFFFFFFF)

// GetSuperPodID returns the supernode identity shared by all accelerators of
// this node. All chips of one node always report the same super-pod id, so
// querying the first device is enough. Returns:
//   - id >= 0: a real supernode identity, stable for the node's lifetime;
//   - -1:      the driver answered the 0xffffffff sentinel (unknown);
//   - -2 with ErrSuperPodUnsupported: the hardware/driver does not expose
//     super-pod info (including dcmi init and device enumeration failures,
//     which the node treats as "no supernode affinity").
func GetSuperPodID() (int32, error) {
	mgr, err := getDcManager()
	if err != nil {
		return -2, fmt.Errorf("%w: dcmi init: %v", ErrSuperPodUnsupported, err)
	}

	_, ids, err := mgr.DcGetLogicIDList()
	if err != nil {
		return -2, fmt.Errorf("%w: get logic ids: %v", ErrSuperPodUnsupported, err)
	}
	if len(ids) == 0 {
		return -2, fmt.Errorf("%w: no devices enumerated", ErrSuperPodUnsupported)
	}

	cardID, deviceID, err := mgr.DcGetCardIDDeviceID(ids[0])
	if err != nil {
		return -2, fmt.Errorf("%w: get card/device id: %v", ErrSuperPodUnsupported, err)
	}

	info, err := mgr.DcGetSuperPodInfo(cardID, deviceID)
	if err != nil {
		return -2, fmt.Errorf("%w: %v", ErrSuperPodUnsupported, err)
	}
	if info.SuperPodId == unknownSuperPodID {
		return -1, nil
	}
	return int32(info.SuperPodId), nil
}
