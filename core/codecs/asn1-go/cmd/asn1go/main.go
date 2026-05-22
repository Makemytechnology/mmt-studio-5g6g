// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package main

import (
	"fmt"
	"os"

	"github.com/mmt/asn1go/pkg/codegen"
	"github.com/mmt/asn1go/pkg/parser"
	"github.com/mmt/asn1go/pkg/resolver"
	"github.com/spf13/cobra"
)

var (
	outDir     string
	pkgName    string
	modulePath string
	encoding   string
	genTests   bool
	verbose    bool
)

func main() {
	root := &cobra.Command{
		Use:   "asn1go [flags] <input.asn1> [input2.asn1 ...]",
		Short: "ASN.1 → Go compiler generating PER/UPER codecs for 3GPP schemas",
		Args:  cobra.MinimumNArgs(1),
		RunE:  run,
	}
	root.Flags().StringVarP(&outDir, "output", "o", "./generated", "output directory")
	root.Flags().StringVarP(&pkgName, "package", "p", "asn1gen", "Go package name for generated code")
	root.Flags().StringVarP(&modulePath, "module", "m", "", "full Go module path (for runtime import)")
	root.Flags().StringVar(&encoding, "encoding", "aper", "encoding rules: aper | uper | both")
	root.Flags().BoolVar(&genTests, "test", false, "generate test files with round-trip checks")
	root.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(_ *cobra.Command, inputs []string) error {
	var srcAll string
	for _, f := range inputs {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		srcAll += "\n" + string(b)
	}

	p := parser.New(srcAll)
	modules := p.ParseModules()
	if errs := p.Errors(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e.Error())
		}
		return fmt.Errorf("parsing failed with %d errors", len(errs))
	}

	if verbose {
		for _, m := range modules {
			fmt.Fprintf(os.Stderr, "module %s: %d assignments\n", m.Name, len(m.Assignments))
		}
	}

	reg := resolver.Build(modules)

	g := codegen.New(reg, codegen.Options{
		OutDir:        outDir,
		PackageName:   pkgName,
		ModulePath:    modulePath,
		Encoding:      encoding,
		GenerateTests: genTests,
	})
	return g.Generate()
}
