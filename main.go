// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Hacked for klog/glog -> logr

package main

import (
	"flag"
	"fmt"
	"go/token"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-logr/glogr"
	"k8s.io/klog/glog"

	"github.com/thockin/klog-to-logr/importer"
	"github.com/thockin/klog-to-logr/fixer"
	"github.com/thockin/klog-to-logr/fixes"
)

// TODO(directxman12): restore the "all fixes" usage logic

type byName []fixer.Fix

func (f byName) Len() int           { return len(f) }
func (f byName) Swap(i, j int)      { f[i], f[j] = f[j], f[i] }
func (f byName) Less(i, j int) bool { return f[i].Name < f[j].Name }

func must(fix fixer.Fix, err error) fixer.Fix {
	if err != nil {
		panic(err.Error())
	}
	return fix
}

var (
	doDiff = flag.Bool("diff", false, "print diffs instead of rewriting files")

	allFixes = []fixer.Fix{
		// TODO(thockin): take this as a CLI argument
		must(fixes.LogrFix(fixes.StandardKlogPkg)),
	}
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: kfix [-diff] [path ...]\n")
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nAvailable fixups are:\n")
	sort.Sort(byName(allFixes))
	for _, f := range allFixes {
		fmt.Fprintf(os.Stderr, "\n%s\n", f.Name)
		desc := strings.TrimSpace(f.Description)
		desc = strings.Replace(desc, "\n", "\n\t", -1)
		fmt.Fprintf(os.Stderr, "\t%s\n", desc)
	}
	os.Exit(93)  // carefully chosen artisinal exit code
}

func main() {
	flag.Usage = usage
	flag.Parse()

	log := glogr.New()
	defer glog.Flush()

	if flag.NArg() == 0 {
		usage()
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Error(err, "where are we?  Nobody knows...")
		os.Exit(1)
	}
	
	imp, loader := importer.NewImporter(cwd, log.WithName("importer"))
	fixHandler := &diffFixHandler{
		fileSet: loader.FileSet(),
		doDiff: *doDiff,
	}
	fixer := &fixer.Fixer{
		Log: log.WithName("fixer"),
		Fixes: allFixes,
		Loader: loader,
		HandleFix: fixHandler.handleFix,
	}

	//FIXME: suport foo.com/repo/pkg/... syntax
	// the go standard library code for this ^ is... a thing
	// it lives at `cmd/go/internal/search/search.go`, holed up
	// living its best life, only working in a couple of commands.
	for i := 0; i < flag.NArg(); i++ {
		arg := flag.Arg(i)
		_, err := imp.Import(arg)
		if err != nil {
			log.Error(err, "unable to import package from argument", "path", arg)
			os.Exit(1)
		}
		pkg := loader.PackageInfoFor(arg)
		pkgLog := log.WithValues("package", pkg.BuildInfo.ImportPath)
		pkgLog.V(1).Info("fixing package")
		if err := fixer.FixPackage(pkg); err != nil {
			fmt.Fprintf(os.Stderr, "aborting package %q: %v\n", arg, err)
			os.Exit(5)
		}
	}

	os.Exit(0)
}

// readFile reads the given named file, returning the contents.
func readFile(filename string) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return src, nil
}

type diffFixHandler struct {
	fileSet *token.FileSet
	doDiff bool
}

func (h *diffFixHandler) handleFix(info fixer.FileInfo) error {
	// Get the original source.
	src, err := readFile(info.Name)
	if err != nil {
		return err
	}
	// Format the AST again.  We did this after each fix, so it appears
	// redundant, but it is necessary to generate gofmt-compatible
	// source code in a few cases. The official gofmt style is the
	// output of the printer run on a standard AST generated by the parser,
	// but the source we generated inside the loop above is the
	// output of the printer run on a mangled AST generated by a fixer.
	newSrc, err := fixer.GofmtFile(info.AST, h.fileSet)
	if err != nil {
		return err
	}

	if h.doDiff {
		data, err := diff(src, newSrc)
		if err != nil {
			return fmt.Errorf("computing diff: %s", err)
		}
		fmt.Printf("diff %s %s\n", info.Name, filepath.Join("fixed", info.Name))
		os.Stdout.Write(data)
		return nil
	}

	return ioutil.WriteFile(info.Name, newSrc, 0)
}

func writeTempFile(dir, prefix string, data []byte) (string, error) {
	file, err := ioutil.TempFile(dir, prefix)
	if err != nil {
		return "", err
	}
	_, err = file.Write(data)
	if err1 := file.Close(); err == nil {
		err = err1
	}
	if err != nil {
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func diff(b1, b2 []byte) (data []byte, err error) {
	f1, err := writeTempFile("", "kfix", b1)
	if err != nil {
		return
	}
	defer os.Remove(f1)

	f2, err := writeTempFile("", "kfix", b2)
	if err != nil {
		return
	}
	defer os.Remove(f2)

	cmd := "diff"
	// NO! NO PLAN9-specific CODE!

	data, err = exec.Command(cmd, "-u", f1, f2).CombinedOutput()
	if len(data) > 0 {
		// diff exits with a non-zero status when the files don't match.
		// Ignore that failure as long as we get output.
		err = nil
	}
	return
}
