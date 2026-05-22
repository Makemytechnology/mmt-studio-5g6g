// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package runtime provides byte-level primitives for 3GPP NAS TLV codec.
// All decode functions are panic-free and return typed errors.
package runtime

import (
	"errors"
	"fmt"
)

var (
	ErrBufferTooShort     = errors.New("nas: buffer too short")
	ErrInvalidLength      = errors.New("nas: invalid IE length")
	ErrInvalidIEI         = errors.New("nas: invalid information element identifier")
	ErrMandatoryIEMissing = errors.New("nas: mandatory IE missing")
	ErrInvalidMessageType = errors.New("nas: invalid message type")
	ErrInvalidEPD         = errors.New("nas: invalid extended protocol discriminator")
	ErrLengthExceeded     = errors.New("nas: IE length exceeds maximum")
	ErrUnknownIEI         = errors.New("nas: unknown IEI")
)

// NASDecodeError wraps a low-level error with message / IE / offset context
// so callers can pinpoint exactly where a malformed PDU failed to decode.
type NASDecodeError struct {
	MessageType string
	IEName      string
	Offset      int
	Err         error
}

func (e *NASDecodeError) Error() string {
	if e.IEName != "" {
		return fmt.Sprintf("nas decode: msg=%s ie=%s offset=%d: %v",
			e.MessageType, e.IEName, e.Offset, e.Err)
	}
	return fmt.Sprintf("nas decode: msg=%s offset=%d: %v",
		e.MessageType, e.Offset, e.Err)
}

func (e *NASDecodeError) Unwrap() error { return e.Err }

// NewDecodeError is a convenience constructor used by generated code.
func NewDecodeError(msg, ie string, offset int, err error) *NASDecodeError {
	return &NASDecodeError{MessageType: msg, IEName: ie, Offset: offset, Err: err}
}
