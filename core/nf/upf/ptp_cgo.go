//go:build linux && cgo

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// cgo shim over the fast-path (g)PTP steering primitive
// (dataplane/src/upf_ptp.c). Kept outside the test file because cgo
// is not permitted in _test.go; exercised by ptp_steering_test.go.
package upf

/*
#include "upf_ptp.h"
*/
import "C"

import "unsafe"

// ptpProcessC runs upf_ptp_process on a copy of pkt inside a buffer of
// the given capacity. Returns the resulting packet and, on egress, the
// residence time applied to correctionField.
func ptpProcessC(pkt []byte, capacity int, ingress bool, nowNS uint64) ([]byte, uint64) {
	buf := make([]byte, capacity)
	copy(buf, pkt)
	ing := C.uint8_t(0)
	if ingress {
		ing = 1
	}
	var residence C.uint64_t
	n := C.upf_ptp_process((*C.uint8_t)(unsafe.Pointer(&buf[0])),
		C.uint16_t(len(pkt)), C.uint32_t(capacity), ing,
		C.uint64_t(nowNS), &residence)
	return buf[:int(n)], uint64(residence)
}
