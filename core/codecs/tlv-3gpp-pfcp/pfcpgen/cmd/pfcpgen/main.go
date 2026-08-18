// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package main

import (
	"fmt"
	"os"

	"github.com/mmt/pfcpgen/pkg/codegen"
	"github.com/mmt/pfcpgen/pkg/schema"
	"github.com/spf13/cobra"
)

func main() {
	var (
		defDir  string
		outDir  string
		pkgName string
		rtPath  string
	)
	root := &cobra.Command{
		Use:   "pfcpgen",
		Short: "PFCP (TS 29.244) TLV codec generator",
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := schema.Load(defDir)
			if err != nil {
				return err
			}
			g := codegen.NewGenerator(repo, outDir, pkgName)
			if rtPath != "" {
				g.RuntimeImport = rtPath
			}
			if err := g.Generate(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "generated %d messages, %d IE types → %s\n",
				len(repo.Messages), len(repo.IETypes), outDir)
			return nil
		},
	}
	root.Flags().StringVarP(&defDir, "definitions", "d", "./definitions", "YAML definitions directory")
	root.Flags().StringVarP(&outDir, "output", "o", "./generated", "output directory")
	root.Flags().StringVarP(&pkgName, "package", "p", "pfcp", "Go package name")
	root.Flags().StringVar(&rtPath, "runtime", "", "override runtime import path")
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
