// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// nasgen is a code generator for 3GPP NAS TLV messages.
//
//	Usage:
//	  nasgen -d ./definitions -o ./generated -p nas
package main

import (
	"fmt"
	"os"

	"github.com/mmt/nasgen/pkg/codegen"
	"github.com/mmt/nasgen/pkg/schema"
	"github.com/spf13/cobra"
)

func main() {
	var (
		defDir    string
		outDir    string
		pkgName   string
		runtimePkg string
		protocol  string
	)

	root := &cobra.Command{
		Use:   "nasgen",
		Short: "3GPP NAS TLV codec generator",
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := schema.Load(defDir)
			if err != nil {
				return err
			}
			// filter by protocol if requested
			if protocol != "all" {
				repo.Messages = filterProtocol(repo.Messages, protocol)
			}
			g := codegen.NewGenerator(repo, outDir, pkgName)
			if runtimePkg != "" {
				g.RuntimeImport = runtimePkg
			}
			if err := g.Generate(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "generated %d messages, %d IE types into %s\n",
				len(repo.Messages), len(repo.IETypes), outDir)
			return nil
		},
	}
	root.Flags().StringVarP(&defDir, "definitions", "d", "./definitions", "YAML definitions directory")
	root.Flags().StringVarP(&outDir, "output", "o", "./generated", "output directory")
	root.Flags().StringVarP(&pkgName, "package", "p", "nas", "Go package name")
	root.Flags().StringVar(&runtimePkg, "runtime", "", "override runtime import path")
	root.Flags().StringVar(&protocol, "protocol", "all", `"5g" | "lte" | "emm" | "esm" | "all"`)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func filterProtocol(msgs []schema.MessageDef, proto string) []schema.MessageDef {
	out := make([]schema.MessageDef, 0, len(msgs))
	for _, m := range msgs {
		switch proto {
		case "5g":
			if m.EPD == 0x7E || m.EPD == 0x2E {
				out = append(out, m)
			}
		case "lte":
			if m.EPD == 0x07 || m.EPD == 0x02 {
				out = append(out, m)
			}
		case "emm":
			if m.EPD == 0x07 {
				out = append(out, m)
			}
		case "esm":
			if m.EPD == 0x02 {
				out = append(out, m)
			}
		default:
			out = append(out, m)
		}
	}
	return out
}
