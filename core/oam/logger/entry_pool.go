// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

// entryPool already exists as a package-level sync.Pool in logger.go
// (initialised in initDefault). The producer Get()s a fresh entry,
// fills the fields, enqueues it; the drainer Put()s it back after
// every sink has consumed the batch.
//
// Zero allocation per log call after warmup is the design goal — see
// invariant I1 in oam/logger/redesign.go.

// getEntry returns a zeroed *Entry from the pool, ready for the
// producer to populate. Slice fields are reused — the producer
// overwrites them.
func getEntry() *Entry {
	e := entryPool.Get().(*Entry)
	// Zero out so a stale value from the pool can't bleed into a
	// new log line. Strings are immutable; setting to "" is cheap.
	e.Seq = 0
	e.TS = e.TS.Truncate(0)
	e.TSFmt = ""
	e.Level = ""
	e.LevelNo = 0
	e.Module = ""
	e.IMSI = ""
	e.Message = ""
	return e
}

// putEntry returns an entry to the pool. Drainer calls this after
// every registered sink has finished processing the batch the entry
// belonged to.
func putEntry(e *Entry) {
	if e == nil {
		return
	}
	entryPool.Put(e)
}
