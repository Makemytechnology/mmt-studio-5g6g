// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package runtime provides byte-level primitives for PFCP (TS 29.244) TLV
// encoding. Unlike NAS (single-byte T/L), PFCP uses 16-bit Type + 16-bit
// Length, supports enterprise-specific IEs (4-byte Enterprise ID injected
// between L and V when the MSB of Type is set) and grouped IEs (recursive
// TLV decode within a value).
package runtime

import (
	"errors"
	"fmt"
)

var (
	ErrBufferTooShort     = errors.New("pfcp: buffer too short")
	ErrInvalidLength      = errors.New("pfcp: invalid IE length")
	ErrInvalidMessageType = errors.New("pfcp: invalid message type")
	ErrInvalidVersion     = errors.New("pfcp: invalid version")
	ErrMandatoryIEMissing = errors.New("pfcp: mandatory IE missing")
)

// DecodeError wraps a low-level error with message + IE context.
type DecodeError struct {
	MessageType string
	IEName      string
	IEType      uint16
	Offset      int
	Err         error
}

func (e *DecodeError) Error() string {
	if e.IEName != "" {
		return fmt.Sprintf("pfcp decode: msg=%s ie=%s(type=%d) offset=%d: %v",
			e.MessageType, e.IEName, e.IEType, e.Offset, e.Err)
	}
	return fmt.Sprintf("pfcp decode: msg=%s offset=%d: %v",
		e.MessageType, e.Offset, e.Err)
}

func (e *DecodeError) Unwrap() error { return e.Err }

func NewDecodeError(msg, ie string, ieType uint16, offset int, err error) *DecodeError {
	return &DecodeError{MessageType: msg, IEName: ie, IEType: ieType, Offset: offset, Err: err}
}
