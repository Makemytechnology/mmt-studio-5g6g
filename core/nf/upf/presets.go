// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// UPF performance tuning presets.
// Port of nf/upf/tuning_presets.py.
package upf

// TuningPreset defines a named set of UPF performance tuning parameters.
type TuningPreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// DPDK EAL
	MbufPoolSize uint32 `json:"mbuf_pool_size"`
	RxRingSize   uint16 `json:"rx_ring_size"`
	TxRingSize   uint16 `json:"tx_ring_size"`
	// Socket tuning
	TunQueueLen   int64 `json:"tun_queue_len"`
	GTPURcvBuf    int64 `json:"gtpu_rcv_buf"`
	GTPUSndBuf    int64 `json:"gtpu_snd_buf"`
	NetdevBacklog int64 `json:"netdev_backlog"`
}

// TuningPresets is the set of predefined presets matching the Python reference.
var TuningPresets = map[string]TuningPreset{
	"minimal": {
		ID: "minimal", Name: "Minimal",
		Description:  "Lowest memory. Suitable for dev/CI.",
		MbufPoolSize: 2048, RxRingSize: 128, TxRingSize: 128,
		TunQueueLen: 500, GTPURcvBuf: 212992, GTPUSndBuf: 212992, NetdevBacklog: 1000,
	},
	"small_vm": {
		ID: "small_vm", Name: "Small VM",
		Description:  "1-2 vCPU, 2-4 GB RAM. Good for lab/PoC.",
		MbufPoolSize: 8192, RxRingSize: 256, TxRingSize: 256,
		TunQueueLen: 1000, GTPURcvBuf: 425984, GTPUSndBuf: 425984, NetdevBacklog: 2000,
	},
	"medium_vm": {
		ID: "medium_vm", Name: "Medium VM",
		Description:  "4-8 vCPU, 8-16 GB RAM. Integration testing.",
		MbufPoolSize: 16384, RxRingSize: 512, TxRingSize: 512,
		TunQueueLen: 2000, GTPURcvBuf: 1048576, GTPUSndBuf: 1048576, NetdevBacklog: 4000,
	},
	"large_bare_metal": {
		ID: "large_bare_metal", Name: "Large Bare Metal",
		Description:  "16+ cores, 32+ GB RAM, DPDK-capable NIC.",
		MbufPoolSize: 65536, RxRingSize: 1024, TxRingSize: 1024,
		TunQueueLen: 5000, GTPURcvBuf: 4194304, GTPUSndBuf: 4194304, NetdevBacklog: 10000,
	},
	"xeon_sriov": {
		ID: "xeon_sriov", Name: "Xeon Platinum SR-IOV",
		Description:  "Xeon Platinum class with SR-IOV/vfio-pci. Consider PMD.",
		MbufPoolSize: 131072, RxRingSize: 2048, TxRingSize: 2048,
		TunQueueLen: 10000, GTPURcvBuf: 8388608, GTPUSndBuf: 8388608, NetdevBacklog: 20000,
	},
}

// RecommendPreset selects the best tuning preset based on environment info.
func RecommendPreset(env map[string]any) string {
	isVM := sv(env, "virtualization") != ""
	ramGB := iv(env, "ram_gb")
	if !isVM && ramGB >= 32 {
		return "large_bare_metal"
	}
	if ramGB >= 16 {
		return "medium_vm"
	}
	if ramGB >= 4 {
		return "small_vm"
	}
	return "minimal"
}

// GetPreset returns a preset by ID, or nil if not found.
func GetPreset(id string) *TuningPreset {
	if p, ok := TuningPresets[id]; ok {
		return &p
	}
	return nil
}

// ListPresets returns all public presets.
func ListPresets() []TuningPreset {
	out := make([]TuningPreset, 0, len(TuningPresets))
	for _, p := range TuningPresets {
		out = append(out, p)
	}
	return out
}
