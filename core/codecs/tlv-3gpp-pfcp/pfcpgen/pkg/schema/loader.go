// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Repo struct {
	Messages []MessageDef
	IETypes  map[string]*IETypeDef
}

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
		var mf MessagesFile
		_ = yaml.Unmarshal(data, &mf)
		r.Messages = append(r.Messages, mf.Messages...)
		var tf IETypesFile
		_ = yaml.Unmarshal(data, &tf)
		for i := range tf.IETypes {
			t := tf.IETypes[i]
			r.IETypes[t.Name] = &t
		}
	}
	return r, r.Validate()
}

func (r *Repo) Validate() error {
	check := func(ctx, name string) error {
		if _, ok := r.IETypes[name]; !ok {
			return fmt.Errorf("schema: %s references undefined IE type %q", ctx, name)
		}
		return nil
	}
	for _, m := range r.Messages {
		for _, ie := range m.IEs {
			if err := check("message "+m.Name+" IE "+ie.Name, ie.TypeRef); err != nil {
				return err
			}
		}
	}
	for _, t := range r.IETypes {
		if !t.Grouped {
			continue
		}
		for _, sub := range t.Members {
			if err := check("grouped IE "+t.Name+" member "+sub.Name, sub.TypeRef); err != nil {
				return err
			}
		}
	}
	return nil
}
