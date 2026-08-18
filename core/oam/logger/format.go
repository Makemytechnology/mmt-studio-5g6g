// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"fmt"
	"strings"
)

// ANSI codes for WARNING / ERROR / CRITICAL on tty sinks. Same values
// the legacy multiHandler used; preserved verbatim so any operator
// pipeline that already does `tail | less -R` renders consistently.
const (
	ansiYellow = "\033[1;33m"
	ansiRed    = "\033[1;31m"
	ansiReset  = "\033[0m"
)

// formatLine renders one Entry into the canonical log line format and
// appends to b. Format (matches the legacy multiHandler exactly):
//
//	YYYY-MM-DD HH:MM:SS:mmm #SEQ LEVEL [module][IMSI:xxx] msg\n
//
// On a colour sink, WARNING / ERROR / CRITICAL are wrapped in ANSI
// codes. SEQ is 8-digit zero-padded for awk-by-column gap detection
// (ddf8a80).
func formatLine(b *strings.Builder, e *Entry, colour bool) {
	if e == nil {
		return
	}
	ts := e.TS.Format("2006-01-02 15:04:05")
	ms := e.TS.Nanosecond() / 1_000_000

	// Entry.Module is already "mmt-core.<sub>" because (*Logger).Get
	// stores the prefixed name; only fall through to bare "mmt-core"
	// if the field is somehow empty.
	modTag := e.Module
	if modTag == "" {
		modTag = "mmt-core"
	}

	imsiTag := ""
	if e.IMSI != "" {
		imsiTag = " [IMSI:" + e.IMSI + "]"
	}

	open, close := "", ""
	if colour {
		switch {
		case e.LevelNo >= int(LevelError):
			open, close = ansiRed, ansiReset
		case e.LevelNo >= int(LevelWarn):
			open, close = ansiYellow, ansiReset
		}
	}

	if open != "" {
		b.WriteString(open)
	}
	fmt.Fprintf(b, "%s:%03d #%08d %-5s [%s]%s %s",
		ts, ms, e.Seq, e.Level, modTag, imsiTag, e.Message)
	if open != "" {
		b.WriteString(close)
	}
	b.WriteByte('\n')
}

// containsIgnoreCase is a case-insensitive substring check used by
// the GUI's module/imsi filter.
func containsIgnoreCase(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
