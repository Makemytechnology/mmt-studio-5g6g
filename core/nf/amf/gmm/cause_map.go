// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Mapping from NAS-codec decode errors to TS 24.501 §5.5.1.2.8(b)
// 5GMM cause codes. Used by every handler that decodes a NAS PDU
// and needs to pick a RegistrationReject / 5GMM STATUS cause based
// on which decode-level sentinel surfaced.
//
// TS 24.501 v19.6.2 §5.5.1.2.8(b) (initial registration abnormal
// case "Protocol error", verbatim from line 28339-28350):
//
//	"If the REGISTRATION REQUEST message is received with a
//	 protocol error, the AMF shall return a REGISTRATION REJECT
//	 message with one of the following 5GMM cause values:
//	    #96 invalid mandatory information;
//	    #99 information element non-existent or not implemented;
//	    #100 conditional IE error; or
//	    #111 protocol error, unspecified."
//
// The same four causes are also valid in 5GMM STATUS bodies via
// §7.5.1 (line 54262-54269).
package gmm

import (
	"errors"

	"github.com/mmt/nasgen/pkg/runtime"
)

// causeForNASDecodeError walks the chain of wrapped sentinel errors
// (NASDecodeError.Unwrap reaches the runtime.Err* values) and picks
// the §5.5.1.2.8(b) cause that best describes the failure. Falls
// back to #111 when the sentinel is unknown — keeps the safe-default
// behaviour the handlers had before the discriminated mapping.
func causeForNASDecodeError(err error) uint8 {
	switch {
	case errors.Is(err, runtime.ErrMandatoryIEMissing):
		// Mandatory IE absent → "invalid mandatory information".
		return CauseInvalidMandatoryInfo
	case errors.Is(err, runtime.ErrUnknownIEI):
		// IEI in the buffer doesn't match any IE this message-type
		// knows about → "information element non-existent or not
		// implemented".
		return CauseIENonExistentOrNotImpl
	case errors.Is(err, runtime.ErrInvalidLength),
		errors.Is(err, runtime.ErrLengthExceeded),
		errors.Is(err, runtime.ErrInvalidIEI):
		// IE present but malformed (length wrong / IEI byte
		// inconsistent with type) → "conditional IE error".
		return CauseConditionalIEError
	}
	return CauseProtocolError
}
