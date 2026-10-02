// Command apitest-gen prepares OpenAPI specs for apitest: it keeps a global
// dictionary of example values per DTO field and parameter, and writes
// missing or invalid examples into the spec, at the places apitest reads
// them.
//
//	apitest-gen -spec openapi.yaml -dict global-dict.json -defaults defaults.json
//
// Run "apitest-gen help" for all commands and flags.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/apply"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/check"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/defaults"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/discover"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/yamldoc"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

const usage = `apitest-gen prepares OpenAPI specs for apitest.

Usage:
  apitest-gen [apply]  -spec <openapi.yaml> [-dict global-dict.json] [-defaults defaults.json] [flags]
  apitest-gen dict     -spec <openapi.yaml> [-dict global-dict.json] [flags]
  apitest-gen discover -defaults defaults.json -base-url <url> [-token-env API_TOKEN] [-out defaults.resolved.json]
  apitest-gen check    -spec <openapi.yaml> [-defaults defaults.json]

Commands:
  apply     (default) update the dictionary, then write missing or invalid
            examples into the spec, in place unless -out is set; with
            -base-url the sources in the defaults are fetched first
  dict      only create or update the dictionary
  discover  fetch the sources in the defaults ({"from": "GET …", "pick": …})
            from a running environment and write the values to -out;
            pass that file to apply after the defaults
  check     report what keeps apitest from running the spec: cases that
            would be NOT_BUILDABLE and examples that violate their schema;
            exit code 1 if there is any (for CI)
  help      show this help

Flags:
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line and returns the exit code: 0 on success,
// 1 on errors, 2 on wrong usage.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		fs, _ := flags("apply", stdout)
		fs.PrintDefaults()
		return 0
	}
	cmd := "apply"
	if !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if cmd != "apply" && cmd != "dict" && cmd != "discover" && cmd != "check" {
		fmt.Fprintf(stderr, "apitest-gen: unknown command %q; run \"apitest-gen help\"\n", cmd)
		return 2
	}
	fs, o := flags(cmd, stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if cmd == "discover" {
		if o.baseURL == "" {
			fmt.Fprintln(stderr, "apitest-gen discover: -base-url is required")
			return 2
		}
		if err := discoverCommand(o, stdout); err != nil {
			fmt.Fprintf(stderr, "apitest-gen discover: %v\n", err)
			return 1
		}
		return 0
	}
	if o.spec == "" {
		fmt.Fprintf(stderr, "apitest-gen %s: -spec is required\n", cmd)
		return 2
	}
	if cmd == "check" {
		return checkCommand(o, stdout, stderr)
	}
	o.dictOnly = cmd == "dict"
	if err := execute(o, stdout); err != nil {
		fmt.Fprintf(stderr, "apitest-gen %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

type options struct {
	spec, dict, defaults, out string
	baseURL, tokenEnv         string
	headers                   headerFlags
	seed                      uint64
	repair, overwrite         bool
	dryRun, verbose, dictOnly bool
	check                     bool
	genericIDs                string
}

func flags(name string, out io.Writer) (*flag.FlagSet, *options) {
	o := &options{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.spec, "spec", "", "OpenAPI file (YAML or JSON), required")
	fs.StringVar(&o.dict, "dict", "global-dict.json", "dictionary file; created if it does not exist")
	fs.StringVar(&o.defaults, "defaults", "defaults.json", "values that win everywhere; several files comma-separated, later ones override earlier ones")
	outHelp := "write the spec here instead of in place (apply)"
	if name == "discover" {
		outHelp = "file for the fetched values"
		o.out = "defaults.resolved.json"
	}
	fs.StringVar(&o.out, "out", o.out, outHelp)
	fs.StringVar(&o.baseURL, "base-url", "", "environment to fetch sources from, e.g. https://api.qa.example/v1")
	fs.StringVar(&o.tokenEnv, "token-env", "", "environment variable holding a bearer token for -base-url")
	fs.Var(&o.headers, "header", `extra header for -base-url, "Name: value"; repeatable`)
	fs.Uint64Var(&o.seed, "seed", 42, "seed for generated values; the same seed gives the same values")
	fs.BoolVar(&o.repair, "repair", false, "regenerate dictionary values that no longer fit their schema")
	fs.BoolVar(&o.overwrite, "overwrite", false, "replace existing valid examples too (apply)")
	fs.StringVar(&o.genericIDs, "generic-ids", "id,uuid,key", "path parameter names that mean another resource on every path")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would change, write nothing")
	fs.BoolVar(&o.verbose, "v", false, "list every change, not only problems")
	fs.BoolVar(&o.check, "check", false, "apply: check the written spec afterwards and exit with 1 on problems")
	return fs, o
}

func execute(o *options, out io.Writer) error {
	s, err := spec.Load(context.Background(), o.spec)
	if err != nil {
		return err
	}
	old, exists, err := dict.Load(o.dict)
	if err != nil {
		return err
	}
	d, notes, st := dict.Build(s, old, dict.Options{Seed: o.seed, Repair: o.repair})

	action := "updated"
	if !exists {
		action = "created"
	}
	fmt.Fprintf(out, "dictionary %s %s: %d DTOs, %d fields, %d parameters; values: %d new, %d reused, %d kept, %d invalid, %d repaired, %d without value\n",
		o.dict, action, st.DTOs, st.Fields, st.Parameters, st.New, st.Reused, st.Kept, st.Invalid, st.Repaired, st.Missing)
	for _, n := range notes {
		if o.verbose || (n.Code != dict.CodeValueNew && n.Code != dict.CodeValueReused) {
			fmt.Fprintf(out, "  %-21s %s: %s\n", n.Code, n.Where, n.Message)
		}
	}
	if o.dictOnly {
		return saveDict(o, d, out)
	}

	defs, err := defaults.LoadAll(o.defaults)
	if err != nil {
		return err
	}
	if sources := unresolved(defs); len(sources) > 0 {
		if o.baseURL == "" {
			fmt.Fprintf(out, "  %-21s %d sources in the defaults are not resolved (%s); pass -base-url or a file from \"apitest-gen discover\"\n", "SOURCE_UNRESOLVED", len(sources), strings.Join(sources, ", "))
		} else if err := resolve(o, defs, out); err != nil {
			return err
		}
	}
	doc, err := yamldoc.Load(o.spec)
	if err != nil {
		return err
	}
	res := apply.Apply(doc, s, d, defs, apply.Options{
		Seed:       o.seed,
		Overwrite:  o.overwrite,
		GenericIDs: strings.Split(o.genericIDs, ","),
	})
	target := o.spec
	if o.out != "" {
		target = o.out
	}
	fmt.Fprintf(out, "spec %s: %d examples added, %d replaced, %d with defaults, %d kept, %d incomplete; %d extensions, %d bindings\n",
		target, res.Stats.Added, res.Stats.Replaced, res.Stats.DefaultsApplied, res.Stats.Kept, res.Stats.Incomplete, res.Stats.Extensions, res.Stats.Bindings)
	for _, n := range res.Notes {
		if o.verbose || !quiet[n.Code] {
			fmt.Fprintf(out, "  %-21s %s: %s\n", n.Code, n.Where, n.Message)
		}
	}
	if defs.Len() > 0 && o.verbose {
		fmt.Fprintln(out, "defaults used:")
		for _, u := range defs.Usage() {
			fmt.Fprintf(out, "  %s: %d\n", u.Key, u.Uses)
		}
	}
	if len(res.Fatal) > 0 {
		for _, f := range res.Fatal {
			fmt.Fprintf(out, "  FATAL %s\n", f)
		}
		return fmt.Errorf("%d problems with the defaults; nothing was written", len(res.Fatal))
	}
	if o.dryRun {
		fmt.Fprintln(out, "dry run: nothing written")
		return nil
	}
	if res.Changed || o.out != "" {
		if err := saveSpec(doc, target); err != nil {
			return err
		}
	}
	if err := saveDict(o, d, out); err != nil {
		return err
	}
	if o.check {
		written, err := spec.Load(context.Background(), target)
		if err != nil {
			return err
		}
		if n := report(check.Run(written, defs.Params()), out); n > 0 {
			return fmt.Errorf("check: %d problems", n)
		}
	}
	return nil
}

// checkCommand runs "apitest-gen check"; exit code 1 means problems.
func checkCommand(o *options, stdout, stderr io.Writer) int {
	s, err := spec.Load(context.Background(), o.spec)
	if err != nil {
		fmt.Fprintf(stderr, "apitest-gen check: %v\n", err)
		return 1
	}
	defs, err := defaults.LoadAll(o.defaults)
	if err != nil {
		fmt.Fprintf(stderr, "apitest-gen check: %v\n", err)
		return 1
	}
	if n := report(check.Run(s, defs.Params()), stdout); n > 0 {
		fmt.Fprintf(stderr, "apitest-gen check: %d problems\n", n)
		return 1
	}
	return 0
}

// report prints a check result and returns the number of problems.
func report(r *check.Result, out io.Writer) int {
	fmt.Fprintf(out, "check: %d of %d cases can be sent, %d problems\n", r.Ready, r.Cases, len(r.Problems))
	for _, p := range r.Problems {
		fmt.Fprintf(out, "  %-15s %s: %s\n", p.Kind, p.Where, p.Message)
	}
	return len(r.Problems)
}

// quiet notes are only listed with -v: they describe intended changes.
var quiet = map[string]bool{
	apply.CodeAdded: true, apply.CodeDefaults: true, apply.CodeGenericID: true,
	apply.CodeExtension: true, apply.CodeBind: true,
}

func saveDict(o *options, d *dict.Dict, out io.Writer) error {
	if o.dryRun {
		if o.dictOnly {
			fmt.Fprintln(out, "dry run: nothing written")
		}
		return nil
	}
	if err := d.Save(o.dict); err != nil {
		return errors.Join(errors.New("writing the dictionary failed"), err)
	}
	return nil
}

// saveSpec writes the document next to its target first and checks that
// apitest can still load it; only then the target is replaced.
func saveSpec(doc *yamldoc.Doc, target string) error {
	b, err := doc.Bytes()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".apitest-gen-*"+filepath.Ext(target))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := spec.Load(context.Background(), tmp.Name()); err != nil {
		return fmt.Errorf("the written spec does not load any more, %s was left unchanged: %w", target, err)
	}
	if info, err := os.Stat(target); err == nil {
		_ = os.Chmod(tmp.Name(), info.Mode().Perm())
	}
	return os.Rename(tmp.Name(), target)
}

// headerFlags collects repeated -header "Name: value" flags.
type headerFlags map[string]string

func (h *headerFlags) String() string { return "" }

func (h *headerFlags) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("header %q must look like \"Name: value\"", v)
	}
	if *h == nil {
		*h = headerFlags{}
	}
	(*h)[strings.TrimSpace(name)] = strings.TrimSpace(value)
	return nil
}

// unresolved lists the source keys without a value.
func unresolved(defs *defaults.Defaults) []string {
	var out []string
	for _, e := range defs.Sources() {
		if e.Value == nil {
			out = append(out, e.Key)
		}
	}
	return out
}

func discoverOptions(o *options) (discover.Options, error) {
	opt := discover.Options{BaseURL: o.baseURL, Headers: o.headers}
	if o.tokenEnv != "" {
		opt.Token = os.Getenv(o.tokenEnv)
		if opt.Token == "" {
			return opt, fmt.Errorf("environment variable %s is empty", o.tokenEnv)
		}
	}
	return opt, nil
}

// resolve fetches the sources in memory for apply.
func resolve(o *options, defs *defaults.Defaults, out io.Writer) error {
	opt, err := discoverOptions(o)
	if err != nil {
		return err
	}
	got, err := discover.Resolve(context.Background(), defs, opt)
	for _, r := range got {
		fmt.Fprintf(out, "  %-21s %s = %s (%s)\n", "SOURCE_RESOLVED", r.Key, jsonText(r.Value), r.Request)
	}
	return err
}

func discoverCommand(o *options, out io.Writer) error {
	defs, err := defaults.LoadAll(o.defaults)
	if err != nil {
		return err
	}
	if len(defs.Sources()) == 0 {
		return fmt.Errorf("%s has no sources ({\"from\": \"GET …\", \"pick\": …})", o.defaults)
	}
	opt, err := discoverOptions(o)
	if err != nil {
		return err
	}
	got, err := discover.Resolve(context.Background(), defs, opt)
	for _, r := range got {
		fmt.Fprintf(out, "%s = %s (%s)\n", r.Key, jsonText(r.Value), r.Request)
	}
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "{\n  \"$comment\": %s", jsonText(fmt.Sprintf("fetched by apitest-gen discover from %s; pass this file to apply after %s", o.baseURL, o.defaults)))
	for _, r := range got {
		fmt.Fprintf(&b, ",\n  %s: %s", jsonText(r.Key), jsonText(r.Value))
	}
	b.WriteString("\n}\n")
	if err := os.WriteFile(o.out, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "written %s\n", o.out)
	return nil
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
