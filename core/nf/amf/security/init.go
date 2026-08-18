// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Hook wiring — resolves the dlnas / pdusetup cycle avoidance: those
// packages expose a `WrapDL` function variable and rely on an init-time
// assignment from this package (which imports them) to plug in the
// real security wrapper. Without this, dlnas.Send / pdusetup.Send
// would ship plain DL NAS even after SMC.
package security

import (
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pdusetup"
)

func init() {
	dlnas.WrapDL = TxDL
	pdusetup.WrapDL = TxDL
}
