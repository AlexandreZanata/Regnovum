// Command i18ngen generates the typed locale catalogs from the locales
// tree (P02-T07). It is wired to make generate / make generate-check:
//
//	i18ngen -check          exit 0 when generated artifacts match; 1 on drift
//	i18ngen                 (re)write generated artifacts
//
// Generated files are never edited manually; the source of truth is
// locales/<locale>/<namespace>.json per I18N_STANDARD.md.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/AlexandreZanata/Regnovum/internal/i18ngen"
)

const usage = `i18ngen generates the typed locale catalogs from locales/.

Usage:

  i18ngen [-check]

Flags:

  -check
	validate that the generated artifacts match the locales tree;
	exit 1 on drift instead of writing anything.

Defaults:

  locales root:  ./locales
  TS target:     web/src/i18n/generated.ts
  Go target:     internal/i18n/generated.go
`

// Output paths fixed by convention, matching the I18N standard layout.
const (
	localesRoot = "locales"
	tsTarget    = "web/src/i18n/generated.ts"
	goTarget    = "internal/i18n/generated.go"
	goPackage   = "i18n"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "i18ngen:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout *os.File) error {
	flags := flag.NewFlagSet("i18ngen", flag.ContinueOnError)
	check := flags.Bool("check", false, "verify generated artifacts instead of writing them")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("invalid flags\n\n%s", usage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n\n%s", flags.Arg(0), usage)
	}

	outputs := i18ngen.Outputs{TSTarget: tsTarget, GoTarget: goTarget, GOPackage: goPackage}
	tsSource, goSource, err := i18ngen.RenderAll(localesRoot, outputs)
	if err != nil {
		return err
	}

	if *check {
		drifted, err := i18ngen.HasDrift(outputs, tsSource, goSource)
		if err != nil {
			return err
		}
		if drifted {
			return fmt.Errorf("generated artifacts are stale; run 'make generate'")
		}
		fmt.Fprintln(stdout, "i18ngen: generated artifacts are up to date")
		return nil
	}

	tsChanged, err := i18ngen.WriteIfChanged(tsTarget, tsSource)
	if err != nil {
		return err
	}
	goChanged, err := i18ngen.WriteIfChanged(goTarget, goSource)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "i18ngen: wrote %s (changed: %v), %s (changed: %v)\n",
		tsTarget, tsChanged, goTarget, goChanged)
	return nil
}
