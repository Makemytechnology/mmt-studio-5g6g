// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//go:build !windows

package logger

import "os"

func init() {
	isTTY = func(f *os.File) bool {
		if f == nil {
			return false
		}
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return fi.Mode()&os.ModeCharDevice != 0
	}
}
