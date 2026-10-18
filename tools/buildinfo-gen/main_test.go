// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fixtureSHA   = "unused"
	goModContent = "module github.com/deadfin-org/deadfin\n\ngo 1.24\n"
)

// fixture is one hermetic module root with an optional Git repository.
type fixture struct {
	root    string
	staging string
	sha     string
}

// newFixture creates a module root (go.mod only) in a temp directory, chdirs
// into it, clears the four identity variables and SOURCE_DATE_EPOCH, and
// isolates Git from the host configuration.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "src")
	staging := filepath.Join(base, "staging")
	for _, dir := range []string{root, staging} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goModContent), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, name := range []string{"REVISION", "VERSION", "CHANNEL", "DIRTY", "SOURCE_DATE_EPOCH", "GIT_DIR", "GIT_WORK_TREE"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", base)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	t.Setenv("GIT_AUTHOR_DATE", "2024-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2024-01-01T00:00:00Z")
	return &fixture{root: root, staging: staging}
}

func (f *fixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepo creates a repository on the named branch with one commit.
func (f *fixture) initRepo(t *testing.T, branch string) {
	t.Helper()
	f.git(t, "init", "-q", "-b", branch)
	f.git(t, "add", "go.mod")
	f.git(t, "commit", "-q", "-m", "base")
	f.sha = f.git(t, "rev-parse", "HEAD")
}

// run invokes the generator and returns exit code, stdout, stderr.
func (f *fixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (f *fixture) runStaged(t *testing.T) (int, string, string) {
	t.Helper()
	return f.run(t, "-o", f.staging)
}

func (f *fixture) assertNoOutput(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging directory must stay empty on failure; found %v", entries)
	}
}

// staged parses and type-checks the staged file and returns the constants.
func (f *fixture) staged(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join(f.staging, "buildinfo.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("staged file does not parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	if _, err := conf.Check("buildinfo", fset, []*ast.File{file}, nil); err != nil {
		t.Fatalf("staged file does not type-check: %v", err)
	}
	values := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				values[name.Name] = exprString(vs.Values[i])
			}
		}
	}
	overlay, err := os.ReadFile(filepath.Join(f.staging, "overlay.json"))
	if err != nil {
		t.Fatalf("overlay.json: %v", err)
	}
	inTree := filepath.Join(f.root, "internal", "buildinfo", "buildinfo.go")
	if !strings.Contains(string(overlay), `"`+inTree+`":"`+path+`"`) {
		t.Fatalf("overlay.json = %s, want Replace %s -> %s", overlay, inTree, path)
	}
	return values
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		return strings.Trim(v.Value, `"`)
	case *ast.Ident:
		return v.Name
	}
	return ""
}

func assertIdentity(t *testing.T, got map[string]string, channel, version, revision, dirty, buildTime string) {
	t.Helper()
	want := map[string]string{"Component": "deadfin-server", "Channel": channel, "Version": version, "Revision": revision, "Dirty": dirty, "BuildTime": buildTime, "CompatibilityTarget": "12.0.0", "APIVersion": "1"}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func setExplicit(t *testing.T, revision, version, channel, dirty string) {
	t.Helper()
	t.Setenv("REVISION", revision)
	t.Setenv("VERSION", version)
	t.Setenv("CHANNEL", channel)
	t.Setenv("DIRTY", dirty)
}

const sha = "0123456789abcdef0123456789abcdef01234567"

func TestExplicitRelease(t *testing.T) {
	f := newFixture(t)
	setExplicit(t, sha, "v0.2.3", "release", "false")
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	code, out, errOut := f.runStaged(t)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "release", "v0.2.3", sha, "false", "2023-11-14T22:13:20Z")
	if !strings.Contains(out, "version=v0.2.3\n") || !strings.Contains(out, "channel=release\n") || !strings.Contains(out, "dirty=false\n") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestExplicitReleaseCandidate(t *testing.T) {
	f := newFixture(t)
	setExplicit(t, sha, "0.3.0-rc.1", "rc", "false")
	code, out, errOut := f.runStaged(t)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "rc", "0.3.0-rc.1", sha, "false", "")
	if !strings.Contains(out, "version=0.3.0-rc.1\n") || !strings.Contains(out, "channel=rc\n") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestExplicitTrainAndMaster(t *testing.T) {
	f := newFixture(t)
	setExplicit(t, sha, "v0.2.3-dev-"+sha[:12], "train", "")
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("train: exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "train", "v0.2.3-dev-"+sha[:12], sha, "false", "")

	setExplicit(t, sha, "dev-"+sha[:12], "master", "0")
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("master: exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "master", "dev-"+sha[:12], sha, "false", "")
}

func TestExplicitRejections(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		message string
	}{
		{"partial env rule 9", map[string]string{"CHANNEL": "release"}, "missing REVISION, VERSION, DIRTY"},
		{"malformed DIRTY", map[string]string{"REVISION": sha, "VERSION": "v0.2.3", "CHANNEL": "release", "DIRTY": "yes"}, "rule 10"},
		{"uppercase REVISION", map[string]string{"REVISION": strings.ToUpper(sha), "VERSION": "v0.2.3", "CHANNEL": "release", "DIRTY": "false"}, "rule 2"},
		{"rc dirty", map[string]string{"REVISION": sha, "VERSION": "0.3.0-rc.1", "CHANNEL": "rc", "DIRTY": "true"}, "rule 4"},
		{"rc with v", map[string]string{"REVISION": sha, "VERSION": "v0.3.0-rc.1", "CHANNEL": "rc", "DIRTY": "false"}, "rule 13"},
		{"release dirty", map[string]string{"REVISION": sha, "VERSION": "v0.2.3", "CHANNEL": "release", "DIRTY": "-dirty"}, "rule 4"},
		{"unknown clean rule 12", map[string]string{"REVISION": "unknown", "VERSION": "0.0.0-local", "CHANNEL": "local", "DIRTY": "false"}, "rule 12"},
		{"train wrong prefix", map[string]string{"REVISION": sha, "VERSION": "0.2.3-dev-ffffffffffff", "CHANNEL": "train", "DIRTY": "false"}, "rule 6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			code, _, errOut := f.runStaged(t)
			if code == 0 || !strings.Contains(errOut, tc.message) {
				t.Fatalf("exit %d, stderr %q; want non-zero mentioning %q", code, errOut, tc.message)
			}
			f.assertNoOutput(t)
		})
	}
}

func TestSourceDateEpoch(t *testing.T) {
	for _, raw := range []string{"abc", "-1", "+1", "", "99999999999999999999", "253402300800"} {
		t.Run("reject "+raw, func(t *testing.T) {
			f := newFixture(t)
			setExplicit(t, sha, "v0.2.3", "release", "false")
			t.Setenv("SOURCE_DATE_EPOCH", raw)
			code, _, errOut := f.runStaged(t)
			if code == 0 || !strings.Contains(errOut, "rule 11") {
				t.Fatalf("exit %d, stderr %q; want rule 11", code, errOut)
			}
			f.assertNoOutput(t)
		})
	}
	f := newFixture(t)
	setExplicit(t, sha, "v0.2.3", "release", "false")
	t.Setenv("SOURCE_DATE_EPOCH", "253402300799")
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "release", "v0.2.3", sha, "false", "9999-12-31T23:59:59Z")
}

func TestDiscoverMaster(t *testing.T) {
	f := newFixture(t)
	f.initRepo(t, "master")
	t.Setenv("SOURCE_DATE_EPOCH", "1704067200")
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "master", "dev-"+f.sha[:12], f.sha, "false", "2024-01-01T00:00:00Z")
}

func TestDiscoverTrainCleanAndDirty(t *testing.T) {
	f := newFixture(t)
	f.initRepo(t, "train/v0.2.3")
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "train", "v0.2.3-dev-"+f.sha[:12], f.sha, "false", "")

	if err := os.WriteFile(filepath.Join(f.root, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("dirty: exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "train", "v0.2.3-dev-"+f.sha[:12], f.sha, "true", "")
}

func TestDiscoverDetachedHead(t *testing.T) {
	f := newFixture(t)
	f.initRepo(t, "master")
	f.git(t, "checkout", "-q", "--detach", "HEAD")
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "local", "0.0.0-local", f.sha, "false", "")
}

func TestDiscoverTagCheckout(t *testing.T) {
	f := newFixture(t)
	f.initRepo(t, "master")
	f.git(t, "tag", "v0.2.3")
	f.git(t, "checkout", "-q", "--detach", "v0.2.3")
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "release", "v0.2.3", f.sha, "false", "")
}

func TestDiscoverNoRepository(t *testing.T) {
	f := newFixture(t)
	// A parent repository must not leak in through GIT_DIR.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "nowhere"))
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "local", "0.0.0-local", "unknown", "true", "")
}

func TestDiscoverGitAbsent(t *testing.T) {
	f := newFixture(t)
	f.initRepo(t, "master")
	t.Setenv("PATH", t.TempDir())
	if code, _, errOut := f.runStaged(t); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	assertIdentity(t, f.staged(t), "local", "0.0.0-local", "unknown", "true", "")
}

func TestFlagsRejectedBeforeAnyRead(t *testing.T) {
	f := newFixture(t)
	f.initRepo(t, "master")
	setExplicit(t, sha, "v0.2.3", "release", "false")
	for _, args := range [][]string{
		{},
		{"--channel=release", "-o", f.staging},
		{"-o", f.staging, "extra"},
		{f.staging},
		{"-o"},
	} {
		code, _, errOut := f.run(t, args...)
		if code != 2 || !strings.Contains(errOut, "usage:") {
			t.Fatalf("args %v: exit %d, stderr %q; want 2 with usage", args, code, errOut)
		}
		f.assertNoOutput(t)
	}
	if status := f.git(t, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("fixture repository modified: %q", status)
	}
}

func TestStagingInsideTreeRejected(t *testing.T) {
	f := newFixture(t)
	inside := filepath.Join(f.root, "staging")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	setExplicit(t, sha, "v0.2.3", "release", "false")
	if code, _, errOut := f.run(t, "-o", inside); code == 0 || !strings.Contains(errOut, "inside the module root") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestWrongModuleRejected(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "go.mod"), []byte("module example.com/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setExplicit(t, sha, "v0.2.3", "release", "false")
	if code, _, errOut := f.runStaged(t); code != 1 || !strings.Contains(errOut, "not module") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	f.assertNoOutput(t)
}

// TestDiscoverMatchesImagePublication proves the Go ref table equals the
// shell mapping in tools/image-publication for master, train, release-line,
// and tag refs.
func TestDiscoverMatchesImagePublication(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "image-publication"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	publication := func(f *fixture, ref string) (version, channel string) {
		cmd := exec.Command(script, ref)
		cmd.Dir = f.root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("image-publication %s: %v\n%s", ref, err, out)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if v, ok := strings.CutPrefix(line, "version="); ok {
				version = v
			}
			if c, ok := strings.CutPrefix(line, "channel="); ok {
				channel = c
			}
		}
		return version, channel
	}
	for _, tc := range []struct{ branch, ref string }{
		{"master", "master"},
		{"train/v0.2.3", "train/v0.2.3"},
		{"v0.4", "v0.4"},
		{"v0.4", "refs/heads/v0.4"},
		{"master", "refs/tags/v0.2.3"},
		{"master", "refs/tags/v0.3.0-rc.1"},
		{"master", "refs/tags/0.3.0-rc.12"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			f := newFixture(t)
			f.initRepo(t, tc.branch)
			f.git(t, "update-ref", "refs/remotes/origin/master", f.sha)
			if strings.HasPrefix(tc.ref, "refs/tags/") {
				f.git(t, "tag", strings.TrimPrefix(tc.ref, "refs/tags/"))
				f.git(t, "checkout", "-q", "--detach", "HEAD")
			}
			if code, _, errOut := f.runStaged(t); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			got := f.staged(t)
			version, channel := publication(f, tc.ref)
			if got["Version"] != version || got["Channel"] != channel {
				t.Fatalf("generator %s/%s, image-publication %s/%s", got["Channel"], got["Version"], channel, version)
			}
		})
	}
}
