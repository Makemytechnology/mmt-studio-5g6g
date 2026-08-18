// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package security

import (
	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
)

// DecipherContainer deciphers a NAS Message Container inner value per
// TS 24.501 v19.6.2 §4.4.6 "Protection of initial NAS signalling
// messages", where the UE packs a RegistrationRequest / Deregistration
// Request / ServiceRequest into the container IE encrypted with its
// current KNASEnc + NEA + UL NAS COUNT.
//
// ulCount is the count that protected the OUTER security-protected
// 5GMM message carrying this container. §4.4.6 case b.1 verbatim:
//
//	"The UE shall cipher the value part of the NAS message container
//	 IE of the initial NAS message using the current NAS ciphering key,
//	 the NAS COUNT value, the NAS connection identifier and the
//	 selected NAS encryption algorithm."
//
// The "NAS COUNT value" here is the same count that protected the
// outer message, so the caller MUST pass the pre-advance UL count
// (available as RxMeta.ULCount from the preceding RxNAS call). Do not
// read ue.Security.ULNasCount directly — after RxNAS it has been
// advanced past the value that protected the container.
//
// When NEA==0 (no cipher) the input is returned unchanged — §4.4.6
// says the value is "ciphered" but NEA0 is a null cipher so no
// transformation is applied.
//
// When the UE has no active ciphering key (pre-SMC cached ctx path),
// the caller passes the cached ctx here; we look up EEA/KNASEnc from
// that ctx's fields.
func DecipherContainer(ue *uectx.AmfUeCtx, ulCount uint32, containerBytes []byte) ([]byte, error) {
	if ue == nil || ue.Security == nil || ue.Security.EEA == 0 {
		return containerBytes, nil
	}
	if len(ue.Security.KNASEnc) != 16 {
		return nil, ErrNoCipherKey
	}
	return nasEncrypt(ue.Security.EEA, ue.Security.KNASEnc, ulCount,
		sacrypto.NASBearerDefault, sacrypto.NASDirUplink, containerBytes)
}
