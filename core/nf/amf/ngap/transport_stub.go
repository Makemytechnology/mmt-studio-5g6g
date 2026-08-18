// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
//go:build !linux

package ngap

import "github.com/mmt/mmt-studio-core/oam/logger"

func platformListen(cfg ListenConfig) (Listener, error) {
	logger.Get("amf.ngap.transport.stub").
		Warnf("NGAP transport: TCP stub (non-Linux build) addr=%s", cfg.Addr)
	return tcpListen(cfg)
}
