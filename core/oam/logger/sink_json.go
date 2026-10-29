// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// jsonSink writes one JSON object per Entry, newline-delimited.
// Replaces the deleted json_handler.go (legacy slog jsonHandler).
// Activated by SACORE_LOG_JSON=1 in initDefault — registered in
// place of consoleSink when the env var is set.
//
// Schema (stable across the project — operators / ELK pipelines
// depend on it):
//
//	{"ts":"2026-04-25T10:21:55.006Z","seq":1,"level":"INFO",
//	 "module":"mmt-core.amf.ngap","imsi":"001011...","msg":"…"}
//
// "imsi" is omitted when empty so structured-log pipelines don't
// see a noisy empty-string field for infrastructure logs.
type jsonSink struct {
	mu sync.Mutex
	w  io.Writer
}

func newJSONSink(w io.Writer) *jsonSink {
	return &jsonSink{w: w}
}

func (s *jsonSink) Name() string { return "json" }

type jsonRecord struct {
	TS     string `json:"ts"`
	Seq    int64  `json:"seq"`
	Level  string `json:"level"`
	Module string `json:"module"`
	IMSI   string `json:"imsi,omitempty"`
	Msg    string `json:"msg"`
}

func (s *jsonSink) Emit(batch []*Entry) {
	if s.w == nil || len(batch) == 0 {
		return
	}
	var b strings.Builder
	b.Grow(160 * len(batch))
	enc := json.NewEncoder(&b)
	for _, e := range batch {
		// time.Format(time.RFC3339Nano) plus a fixed UTC suffix would
		// break callers expecting localtime ts; match the wire format
		// the previous jsonHandler used (RFC3339Nano in local zone).
		rec := jsonRecord{
			TS:     e.TS.Format(time.RFC3339Nano),
			Seq:    e.Seq,
			Level:  e.Level,
			Module: e.Module,
			IMSI:   e.IMSI,
			Msg:    e.Message,
		}
		_ = enc.Encode(&rec) // Encoder appends '\n' itself
	}
	s.mu.Lock()
	_, _ = io.WriteString(s.w, b.String())
	s.mu.Unlock()
}

func (s *jsonSink) Flush() error { return nil }
func (s *jsonSink) Close() error { return nil }
