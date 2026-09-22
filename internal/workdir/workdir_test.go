package workdir

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/pathguard"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/mcpserver"
	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// The judgement is nlink-jp/pathguard's and tested there. These tests cover
// what this adapter owns — taking _meta from the context, carrying errors onto
// toolerr, the server's own directory, a zero value refusing — and the
// behaviour this server's callers rely on.

func code(t *testing.T, err error) string {
	t.Helper()
	var te *toolerr.Error
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not a structured tool error", err)
	}
	return te.Code
}

func metaCtx(t *testing.T, value any) context.Context {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return mcpserver.WithRequestMeta(context.Background(),
		map[string]json.RawMessage{MetaKey: raw})
}

func resolver(t *testing.T) Resolver {
	t.Helper()
	return NewResolver(t.TempDir())
}

// TestResolveArgumentWins: the argument is the caller's own statement of where
// it can read files back; a runtime hint is a default beneath it.
func TestResolveArgumentWins(t *testing.T) {
	arg := t.TempDir()
	hint := t.TempDir()
	got, err := resolver(t).Resolve(metaCtx(t, hint), arg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != resolve(t, arg) {
		t.Errorf("Resolve = %q, want %q", got, arg)
	}
}

func TestResolveFallsBackToRequestMeta(t *testing.T) {
	hint := t.TempDir()
	got, err := resolver(t).Resolve(metaCtx(t, hint), "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != resolve(t, hint) {
		t.Errorf("Resolve = %q, want %q", got, hint)
	}
}

// TestResolveWithoutEitherChannelIsAnError pins the whole point of the
// contract: there is no server-owned default to fall through to. The message
// keeps this server's sentence word for word.
func TestResolveWithoutEitherChannelIsAnError(t *testing.T) {
	_, err := resolver(t).Resolve(context.Background(), "")
	if got := code(t, err); got != toolerr.CodeWorkDirRequired {
		t.Errorf("code = %q, want %q", got, toolerr.CodeWorkDirRequired)
	}
	want := "work_dir is required: pass the absolute path of a directory you can read back " +
		"(your session or working directory). Results come back as paths, and a path you cannot open is worth nothing."
	var te *toolerr.Error
	if errors.As(err, &te) && te.Message != want {
		t.Errorf("message = %q\nwant      %q", te.Message, want)
	}
}

func TestResolveRejectsNonStringMeta(t *testing.T) {
	_, err := resolver(t).Resolve(metaCtx(t, 42), "")
	if got := code(t, err); got != toolerr.CodeWorkDirInvalid {
		t.Errorf("code = %q, want %q", got, toolerr.CodeWorkDirInvalid)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"tilde", "~/work", toolerr.CodeWorkDirInvalid},
		{"relative", "work/dir", toolerr.CodeWorkDirInvalid},
		{"parent segment", dir + "/../elsewhere", toolerr.CodeWorkDirInvalid},
		{"missing", filepath.Join(dir, "not-there"), toolerr.CodeWorkDirNotFound},
		{"a file", file, toolerr.CodeWorkDirNotFound},
		{"system tree", "/usr/bin", toolerr.CodeWorkDirDenied},
		{"filesystem root", "/", toolerr.CodeWorkDirDenied},
		{"home itself", home, toolerr.CodeWorkDirDenied},
	}
	r := resolver(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := r.Validate(c.in)
			if err == nil {
				t.Fatalf("Validate(%q) succeeded", c.in)
			}
			if got := code(t, err); got != c.want {
				t.Errorf("code = %q, want %q", got, c.want)
			}
		})
	}
}

// TestValidateAcceptsATemporaryDirectory guards against an over-broad deny
// list: on darwin a per-user temporary directory resolves under /private/var,
// and Codex names exactly that as a place it can write.
func TestValidateAcceptsATemporaryDirectory(t *testing.T) {
	dir := t.TempDir()
	got, err := resolver(t).Validate(dir)
	if err != nil {
		t.Fatalf("Validate(%q): %v", dir, err)
	}
	if got != resolve(t, dir) {
		t.Errorf("Validate = %q, want the symlink-resolved %q", got, resolve(t, dir))
	}
}

// The server's own data directory is refused as a work directory, under any
// spelling, with the reason in the details; a sibling sharing its prefix is
// not.
func TestValidateRefusesTheServersOwnDirectory(t *testing.T) {
	own := resolve(t, t.TempDir())
	inside := filepath.Join(own, "workspaces")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(own)
	for _, dir := range []string{own, inside, strings.ToUpper(inside)} {
		_, err := r.Validate(dir)
		if code(t, err) != toolerr.CodeWorkDirDenied {
			t.Errorf("Validate(%q) = %v, want work_dir_denied", dir, err)
			continue
		}
		var te *toolerr.Error
		if errors.As(err, &te) && te.Details["reason"] != "server_dir" {
			t.Errorf("Validate(%q) details = %v, want reason server_dir", dir, te.Details)
		}
	}
	sibling := own + "-elsewhere"
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(sibling) })
	if _, err := r.Validate(sibling); err != nil {
		t.Errorf("Validate(%q) = %v, want accepted", sibling, err)
	}
}

// A resolver that was never built refuses rather than protecting nothing, and
// so does one given an empty server directory.
func TestAResolverThatWasNotBuiltRefuses(t *testing.T) {
	dir := t.TempDir()
	for name, r := range map[string]Resolver{"zero": {}, "empty server dir": NewResolver("")} {
		if _, err := r.Validate(dir); code(t, err) != toolerr.CodeWorkDirDenied {
			t.Errorf("%s: Validate = %v, want work_dir_denied", name, err)
		}
	}
}

func TestValidateRejectsUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver(t).Validate(dir); code(t, err) != toolerr.CodeWorkDirNotWritable {
		t.Errorf("Validate(%q) = %v, want work_dir_not_writable", dir, err)
	}
}

// resolve mirrors what Validate returns for a good path, so assertions do not
// have to care that /var and /tmp are symlinks on darwin.
func resolve(t *testing.T, dir string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Every server directory given is protected, not only the first.
func TestEveryServerDirectoryIsProtected(t *testing.T) {
	a, b := resolve(t, t.TempDir()), resolve(t, t.TempDir())
	r := NewResolver(a, b)
	for _, dir := range []string{a, b} {
		if _, err := r.Validate(dir); code(t, err) != toolerr.CodeWorkDirDenied {
			t.Errorf("Validate(%q) = %v, want work_dir_denied", dir, err)
		}
	}
}

// A place names its own reason and sentence; LocalPath refuses the home's
// credential places and the place, OutboundPath also refuses credential names
// wherever they sit (an upload leaves the machine).
func TestPlacesAndTheTwoFilePolicies(t *testing.T) {
	home := resolve(t, t.TempDir())
	t.Setenv("HOME", home)
	profile := resolve(t, t.TempDir())
	r := NewResolverFor(pathguard.Place{Path: profile, Kind: pathguard.Protected, Reason: "browser_profile", Why: "it is inside your own Chrome profile"})
	cookies := filepath.Join(profile, "Default", "Cookies")
	if reason, why := r.LocalPath(cookies, cookies); reason != "browser_profile" || !strings.Contains(why, "Chrome profile") {
		t.Errorf("LocalPath(profile cookies) = %q, %q", reason, why)
	}
	key := filepath.Join(home, ".ssh", "id_rsa")
	if reason, _ := r.LocalPath(key, key); reason != "sensitive_path" {
		t.Errorf("LocalPath(~/.ssh/id_rsa) reason = %q, want sensitive_path", reason)
	}
	evidence := "/srv/evidence/home/bob/.ssh/id_rsa"
	if _, why := r.LocalPath(evidence, evidence); why != "" {
		t.Errorf("LocalPath refused an evidence copy: %s", why)
	}
	if reason, _ := r.OutboundPath(evidence, evidence); reason != "sensitive_path" {
		t.Errorf("OutboundPath(evidence copy) reason = %q, want sensitive_path", reason)
	}
}
