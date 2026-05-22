// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// udr/fiveqi.go — Standardized 5QI table (TS 23.501 Table 5.7.4-1).
//
// Go port of nf/udr/fiveQi_table.py. Provides the static 5QI-to-QoS
// characteristic mapping used by SMF and PCF for QoS flow setup.
package udr

// FiveQIInfo describes the QoS characteristics for a standardized 5QI value.
type FiveQIInfo struct {
	FiveQI           int
	ResourceType     string // "GBR" | "Non-GBR"
	DefaultPriority  int
	PacketDelayBudMs int
	PacketErrorRate  string
	ExampleServices  string
}

// fiveQITable is the standardized 5QI table per TS 23.501 Table 5.7.4-1.
var fiveQITable = map[int]FiveQIInfo{
	1:  {1, "GBR", 2, 100, "1e-2", "Conversational Voice"},
	2:  {2, "GBR", 4, 150, "1e-3", "Conversational Video (Live Streaming)"},
	3:  {3, "GBR", 3, 50, "1e-3", "Real-Time Gaming"},
	4:  {4, "GBR", 5, 300, "1e-6", "Non-Conversational Video (Buffered Streaming)"},
	5:  {5, "GBR", 1, 100, "1e-6", "IMS Signaling"},
	6:  {6, "Non-GBR", 6, 300, "1e-6", "Mission Critical Push To Talk (MCPTT) Signaling"},
	7:  {7, "Non-GBR", 7, 100, "1e-3", "Voice, Video, Interactive Gaming"},
	8:  {8, "Non-GBR", 8, 300, "1e-6", "Buffered Video Streaming"},
	9:  {9, "Non-GBR", 9, 300, "1e-6", "General Data"},
	65: {65, "GBR", 0, 1, "1e-5", "Ultra-Reliable Low Latency Communication (URLLC)"},
	66: {66, "GBR", 2, 100, "1e-2", "Vehicle-to-Everything (V2X) Messaging"},
	67: {67, "GBR", 3, 10, "1e-4", "V2X Sensor Sharing"},
	75: {75, "Non-GBR", 5, 10, "1e-6", "Mission-Critical Video"},
	79: {79, "Non-GBR", 6, 50, "1e-6", "Mission-Critical Data"},
}

// GetFiveQIInfo returns the QoS characteristics for a 5QI value, or nil if unknown.
func GetFiveQIInfo(fiveQI int) *FiveQIInfo {
	info, ok := fiveQITable[fiveQI]
	if !ok {
		return nil
	}
	return &info
}

// FiveQITable returns a copy of the full standardized 5QI table.
func FiveQITable() map[int]FiveQIInfo {
	out := make(map[int]FiveQIInfo, len(fiveQITable))
	for k, v := range fiveQITable {
		out[k] = v
	}
	return out
}
