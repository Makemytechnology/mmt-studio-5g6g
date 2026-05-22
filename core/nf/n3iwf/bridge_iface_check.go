// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package n3iwf

import (
	"github.com/mmt/mmt-studio-core/nf/n3iwf/handler"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/n2"
)

// Compile-time check: *n2.BridgeAdapter satisfies handler.NASBridge.
var _ handler.NASBridge = (*n2.BridgeAdapter)(nil)
