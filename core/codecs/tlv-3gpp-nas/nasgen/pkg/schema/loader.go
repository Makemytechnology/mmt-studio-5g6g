// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Repo — loaded collection of messages and IE types, indexed by name.
type Repo struct {
	Messages []MessageDef
	IETypes  map[string]*IETypeDef
}

// Load reads every *.yaml file in dir. Files containing `messages:` populate
// Messages; files containing `ie_types:` populate IETypes.
func Load(dir string) (*Repo, error) {
	r := &Repo{IETypes: map[string]*IETypeDef{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("schema: read dir %q: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("schema: read %q: %w", path, err)
		}
		// Try both shapes — one file may contain only one shape.
		var mf MessagesFile
		_ = yaml.Unmarshal(data, &mf)
		if len(mf.Messages) > 0 {
			r.Messages = append(r.Messages, mf.Messages...)
		}
		var tf IETypesFile
		_ = yaml.Unmarshal(data, &tf)
		for i := range tf.IETypes {
			t := tf.IETypes[i]
			r.IETypes[t.Name] = &t
		}
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// Validate checks that every IE referenced by a message has a type definition,
// and that formats / IEI strings are well-formed.
func (r *Repo) Validate() error {
	for _, m := range r.Messages {
		for _, ie := range m.IEs {
			if _, ok := r.IETypes[ie.TypeRef]; !ok {
				return fmt.Errorf("schema: message %s IE %s references undefined type %s",
					m.Name, ie.Name, ie.TypeRef)
			}
			if ie.IEI != nil {
				if _, _, err := ParseIEI(*ie.IEI); err != nil {
					return fmt.Errorf("schema: message %s IE %s: %w", m.Name, ie.Name, err)
				}
			}
			switch ie.Format {
			case "V", "TV", "LV", "TLV", "LV-E", "TLV-E", "T":
			default:
				return fmt.Errorf("schema: message %s IE %s has unknown format %q",
					m.Name, ie.Name, ie.Format)
			}
		}
	}
	return nil
}

// ParseIEI parses an IEI string.
// Returns (byte value, halfOctet?). "10" → (0x10, false); "B-" → (0xB0, true).
func ParseIEI(s string) (uint8, bool, error) {
	if len(s) == 2 && s[1] == '-' {
		v, err := strconv.ParseUint(string(s[0]), 16, 8)
		if err != nil {
			return 0, false, fmt.Errorf("invalid half-octet IEI %q", s)
		}
		return uint8(v) << 4, true, nil
	}
	v, err := strconv.ParseUint(s, 16, 8)
	if err != nil {
		return 0, false, fmt.Errorf("invalid IEI %q: %w", s, err)
	}
	return uint8(v), false, nil
}
