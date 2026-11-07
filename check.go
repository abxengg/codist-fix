package fix

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Check is whether a prepared fix still compiles and passes the tests, tried in a separate copy of the code so the
// working tree is never touched. Status is passed, failed or unavailable (no Maven project, or no Maven).
type Check struct {
	Status string `json:"status"`
	Step   string `json:"step"` // compile or tests
	Output string `json:"output,omitempty"`
	TookMs int64  `json:"tookMs"`
}

const checkTimeout = 6 * time.Minute

// Check builds the fixed code of the module the fix touches, then runs its tests.
func (f *Fixer) Check(ctx context.Context, id string) Check {
	start := time.Now()
	done := func(status, step, out string) Check {
		return Check{Status: status, Step: step, Output: tail(out, 40), TookMs: time.Since(start).Milliseconds()}
	}
	p := f.get(id)
	if p == nil {
		return done("unavailable", "", "That fix is no longer prepared.")
	}
	mvn, err := exec.LookPath("mvn")
	if err != nil {
		return done("unavailable", "", "Maven is not installed, so the fix could not be built.")
	}
	dir, err := os.MkdirTemp("", "codist-fixcheck-")
	if err != nil {
		return done("unavailable", "", err.Error())
	}
	defer os.RemoveAll(dir)
	var tarball bytes.Buffer
	archive := exec.Command("git", "-C", f.Repo.Root, "archive", "--format=tar", p.base)
	archive.Stdout = &tarball
	if err := archive.Run(); err != nil {
		return done("unavailable", "", "git archive: "+err.Error())
	}
	tar := exec.Command("tar", "-x", "-C", dir)
	tar.Stdin = &tarball
	if out, err := tar.CombinedOutput(); err != nil {
		return done("unavailable", "", string(out))
	}
	for pth, src := range p.changed {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(pth)), src, 0o644); err != nil {
			return done("unavailable", "", err.Error())
		}
	}
	module := moduleOf(dir, p.Path)
	if module == "" {
		return done("unavailable", "", "There is no Maven project for this file, so the fix could not be built.")
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	run := func(goal string) (string, error) {
		args := []string{"-q", "-B", goal}
		if module != "." {
			args = []string{"-q", "-B", "-pl", module, "-am", goal}
		}
		if goal == "test" {
			args = append(args, "-Dsurefire.failIfNoSpecifiedTests=false")
		}
		cmd := exec.CommandContext(ctx, mvn, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return string(out), errors.New("timed out")
		}
		return string(out), err
	}
	if out, err := run("compile"); err != nil {
		return done("failed", "compile", out)
	}
	if out, err := run("test"); err != nil {
		return done("failed", "tests", out)
	}
	return done("passed", "tests", "")
}

// moduleOf is the Maven module holding path, relative to dir ("." for the root), or "" without a pom.xml.
func moduleOf(dir, path string) string {
	d := filepath.Dir(filepath.FromSlash(path))
	for {
		if _, err := os.Stat(filepath.Join(dir, d, "pom.xml")); err == nil {
			return filepath.ToSlash(d)
		}
		if d == "." || d == string(filepath.Separator) {
			return ""
		}
		d = filepath.Dir(d)
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
