// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//go:build windows

package logger

import "os"

func init() {
	// On Windows the console typically does not honor raw ANSI unless VT mode
	// is enabled; stay conservative and disable colour by default.
	isTTY = func(f *os.File) bool { return false }
}
