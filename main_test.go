package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	oldExecLookPath := execLookPath
	execLookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	code := m.Run()
	execLookPath = oldExecLookPath
	os.Exit(code)
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("GG_CONFIG", filepath.Join(t.TempDir(), "config"))
	t.Setenv("HOME", "/tmp/gg-home")

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if cfg.Root != "/tmp/gg-home/src" {
		t.Fatalf("cfg.Root = %q, want %q", cfg.Root, "/tmp/gg-home/src")
	}

	if cfg.Host != "github.com" {
		t.Fatalf("cfg.Host = %q, want github.com", cfg.Host)
	}
}

func TestMainSuccess(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"gg", "version"}

	output, err := captureStdout(t, func() error {
		main()
		return nil
	})
	if err != nil {
		t.Fatalf("captureStdout() error = %v", err)
	}
	if strings.TrimSpace(output) != version {
		t.Fatalf("main() output = %q, want %q", strings.TrimSpace(output), version)
	}
}

func TestLoadConfigFromXDGPath(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	t.Setenv("HOME", "/tmp/gg-home")

	path := filepath.Join(xdgHome, "gg", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	data := `{
  "root": "~/work/src",
  "aliases": {
    "f": "ForkbombEu",
    "credim": "credimi",
    "fc": "ForkbombEu/credimi"
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, gotPath, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if gotPath != path {
		t.Fatalf("config path = %q, want %q", gotPath, path)
	}

	if cfg.Root != "/tmp/gg-home/work/src" {
		t.Fatalf("cfg.Root = %q, want %q", cfg.Root, "/tmp/gg-home/work/src")
	}

	if cfg.Aliases["f"] != "ForkbombEu" {
		t.Fatalf("owner alias missing after load")
	}
}

func TestResolveRepoWithSegmentAliases(t *testing.T) {
	cfg := Config{
		Root: "/tmp/src",
		Host: "github.com",
		Aliases: map[string]string{
			"f":      "ForkbombEu",
			"credim": "credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"f", "credim"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}
}

func TestResolveRepoWithSlashAndFullAlias(t *testing.T) {
	cfg := Config{
		Root: "/tmp/src",
		Host: "github.com",
		Aliases: map[string]string{
			"f":         "ForkbombEu",
			"credim":    "credimi",
			"fc":        "ForkbombEu/credimi",
			"f/credimi": "ForkbombEu/credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"f/credim"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}

	request, err = resolveRequest(cfg, []string{"fc"})
	if err != nil {
		t.Fatalf("resolveRequest() with full alias error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("repo from full alias = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}
}

func TestResolveRepoWithFullAliasTargetUsingSegmentAlias(t *testing.T) {
	cfg := Config{
		Root: "/tmp/src",
		Host: "github.com",
		Aliases: map[string]string{
			"f":       "ForkbombEu",
			"credimi": "f/credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"credimi"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("repo from nested alias = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}
}

func TestResolveRequestSupportsOwnerPath(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"f": "ForkbombEu",
		},
	}

	request, err := resolveRequest(cfg, []string{"ForkbombEu"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Target.Kind != TargetOwner || request.Owner != "ForkbombEu" {
		t.Fatalf("request = %#v, want owner ForkbombEu", request)
	}

	request, err = resolveRequest(cfg, []string{"f"})
	if err != nil {
		t.Fatalf("resolveRequest() with alias error = %v", err)
	}

	if request.Target.Kind != TargetOwner || request.Owner != "ForkbombEu" {
		t.Fatalf("request = %#v, want owner alias ForkbombEu", request)
	}
}

func TestResolveRequestCanonicalizesExistingRepoCase(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root: root,
		Host: "github.com",
	}

	existing := filepath.Join(root, "github.com", "ForkbombEu", "credimi")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	request, err := resolveRequest(cfg, []string{"forkbombeu/credimi"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.Owner != "ForkbombEu" || request.Repo.Name != "credimi" {
		t.Fatalf("request.Repo = %#v, want ForkbombEu/credimi", request.Repo)
	}
}

func TestResolveRequestCanonicalizesExistingOwnerCase(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root: root,
		Host: "github.com",
	}

	if err := os.MkdirAll(filepath.Join(root, "github.com", "ForkbombEu"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	request, err := resolveRequest(cfg, []string{"forkbombeu"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Target.Kind != TargetOwner || request.Owner != "ForkbombEu" {
		t.Fatalf("request = %#v, want owner ForkbombEu", request)
	}
}

func TestResolveTwoArgsRejectsSlashAliasInSegmentPosition(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"f": "ForkbombEu/credimi",
		},
	}

	if _, err := resolveTwoArgs(cfg, "f", "repo"); err == nil {
		t.Fatal("resolveTwoArgs() error = nil, want error")
	}
}

func TestAliasCommandWritesConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gg", "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", "/tmp/gg-home")

	if err := aliasCommand([]string{"ForkbombEu/credimi", "credim"}); err != nil {
		t.Fatalf("aliasCommand() error = %v", err)
	}

	cfg, gotPath, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if gotPath != configPath {
		t.Fatalf("config path = %q, want %q", gotPath, configPath)
	}

	if cfg.Aliases["credim"] != "ForkbombEu/credimi" {
		t.Fatalf("cfg.Aliases[credim] = %q, want %q", cfg.Aliases["credim"], "ForkbombEu/credimi")
	}
}

func TestResolveRequestKeepsOwnerRepoTwoArgForm(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"f":      "ForkbombEu",
			"credim": "credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"f", "credim"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("request.Repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}

	if request.Target.Kind != TargetRepo {
		t.Fatalf("request.Target.Kind = %v, want TargetRepo", request.Target.Kind)
	}
}

func TestResolveRequestTreatsRepoAliasPlusNameAsWorktree(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"credimi": "ForkbombEu/credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"credimi", "newworktree"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("request.Repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}

	if request.Target.Kind != TargetWorktree || request.Target.WorktreeName != "newworktree" {
		t.Fatalf("request.Target = %#v, want worktree newworktree", request.Target)
	}
}

func TestResolveRequestTreatsRepoAliasPlusPRAsPRCheckout(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"credimi": "ForkbombEu/credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"credimi", "99"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Target.Kind != TargetPR || request.Target.PRNumber != 99 {
		t.Fatalf("request.Target = %#v, want PR 99", request.Target)
	}

	request, err = resolveRequest(cfg, []string{"credimi", "#42"})
	if err != nil {
		t.Fatalf("resolveRequest() with #42 error = %v", err)
	}

	if request.Target.Kind != TargetPR || request.Target.PRNumber != 42 {
		t.Fatalf("request.Target = %#v, want PR 42", request.Target)
	}
}

func TestResolveRequestSupportsOwnerRepoPlusTarget(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"f":      "ForkbombEu",
			"credim": "credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"f", "credim", "feature-x"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("request.Repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}

	if request.Target.Kind != TargetWorktree || request.Target.WorktreeName != "feature-x" {
		t.Fatalf("request.Target = %#v, want worktree feature-x", request.Target)
	}
}

func TestResolveNestedRepoPath(t *testing.T) {
	repoPath := "/tmp/src/github.com/ForkbombEu/credimi"

	path, err := resolveNestedRepoPath(repoPath, "worktrees", "feature/test")
	if err != nil {
		t.Fatalf("resolveNestedRepoPath() error = %v", err)
	}

	want := filepath.Join(repoPath, "worktrees", "feature", "test")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestResolveNestedRepoPathRejectsEscape(t *testing.T) {
	if _, err := resolveNestedRepoPath("/tmp/repo", "worktrees", "../oops"); err == nil {
		t.Fatal("resolveNestedRepoPath() error = nil, want error")
	}
}

func TestEnsureOwnerPath(t *testing.T) {
	cfg := Config{
		Root: t.TempDir(),
		Host: "github.com",
	}

	path, err := ensureOwnerPath(cfg, "ForkbombEu")
	if err != nil {
		t.Fatalf("ensureOwnerPath() error = %v", err)
	}

	want := filepath.Join(cfg.Root, "github.com", "ForkbombEu")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestEnsureRepoStoreUsesExistingLocalDirectory(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root: root,
		Host: "github.com",
	}
	repo := Repo{Owner: "puria", Name: "gg"}
	localPath := filepath.Join(root, "github.com", "puria", "gg")

	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localPath, "README.md"), []byte("local only\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}

	if store.Managed {
		t.Fatal("store.Managed = true, want false for existing local directory")
	}
	if store.MainPath != localPath {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, localPath)
	}
}

func TestEnsureRequestReturnsExistingLocalDirectory(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root: root,
		Host: "github.com",
	}
	localPath := filepath.Join(root, "github.com", "puria", "gg")

	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localPath, ".codex"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	path, err := ensureRequest(cfg, Request{
		Repo: Repo{Owner: "puria", Name: "gg"},
		Target: Target{
			Kind: TargetRepo,
		},
	})
	if err != nil {
		t.Fatalf("ensureRequest() error = %v", err)
	}

	if path != localPath {
		t.Fatalf("path = %q, want %q", path, localPath)
	}
}

func TestShellInitEmbedsExecutablePath(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) {
		return "/tmp/bin/gg", nil
	}
	defer func() {
		executablePath = oldExecutablePath
	}()

	script, err := shellInit("fish")
	if err != nil {
		t.Fatalf("shellInit() error = %v", err)
	}

	if !strings.Contains(script, `/tmp/bin/gg`) {
		t.Fatalf("shell script does not embed executable path: %q", script)
	}
	if strings.Contains(script, "command gg") {
		t.Fatalf("shell script still references command gg: %q", script)
	}
}

func TestShellInitPassesThroughManagementAliases(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) {
		return "/tmp/bin/gg", nil
	}
	defer func() {
		executablePath = oldExecutablePath
	}()

	fishScript, err := shellInit("fish")
	if err != nil {
		t.Fatalf("shellInit(fish) error = %v", err)
	}
	if !strings.Contains(fishScript, "new list ls status starship prune rm") {
		t.Fatalf("fish shell script missing new/ls/rm passthrough: %q", fishScript)
	}

	bashScript, err := shellInit("bash")
	if err != nil {
		t.Fatalf("shellInit(bash) error = %v", err)
	}
	if !strings.Contains(bashScript, "|new|list|ls|status|starship|prune|rm)") {
		t.Fatalf("bash shell script missing new/ls/rm passthrough: %q", bashScript)
	}
}

func TestRepoHasRefs(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, "repo.git")

	if err := exec.Command("git", "init", "--bare", gitDir).Run(); err != nil {
		t.Fatalf("git init --bare error = %v", err)
	}

	hasRefs, err := repoHasRefs(gitDir)
	if err != nil {
		t.Fatalf("repoHasRefs() error = %v", err)
	}
	if hasRefs {
		t.Fatal("repoHasRefs() = true, want false for empty bare repo")
	}

	worktree := filepath.Join(root, "wt")
	if err := exec.Command("git", "--git-dir", gitDir, "worktree", "add", "--orphan", "-b", "main", worktree).Run(); err != nil {
		t.Fatalf("git worktree add error = %v", err)
	}
	if err := exec.Command("git", "-C", worktree, "config", "user.name", "Test User").Run(); err != nil {
		t.Fatalf("git config user.name error = %v", err)
	}
	if err := exec.Command("git", "-C", worktree, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatalf("git config user.email error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := exec.Command("git", "-C", worktree, "add", "README.md").Run(); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if err := exec.Command("git", "-C", worktree, "commit", "-m", "initial").Run(); err != nil {
		t.Fatalf("git commit error = %v", err)
	}

	hasRefs, err = repoHasRefs(gitDir)
	if err != nil {
		t.Fatalf("repoHasRefs() after commit error = %v", err)
	}
	if !hasRefs {
		t.Fatal("repoHasRefs() = false, want true after commit")
	}
}

func TestListRepoEntriesManaged(t *testing.T) {
	root := t.TempDir()
	store := RepoStore{
		ContainerPath: root,
		MainPath:      filepath.Join(root, "main"),
		Managed:       true,
	}

	for _, path := range []string{
		store.MainPath,
		filepath.Join(root, "worktrees", "feature-x"),
		filepath.Join(root, "PR", "99"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		t.Fatalf("listRepoEntries() error = %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}

	got := []string{
		entries[0].Kind + ":" + entries[0].Name,
		entries[1].Kind + ":" + entries[1].Name,
		entries[2].Kind + ":" + entries[2].Name,
	}
	want := []string{"main:main", "pr:99", "worktree:feature-x"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestListRepoEntriesLocal(t *testing.T) {
	store := RepoStore{
		MainPath: "/tmp/local-repo",
		Managed:  false,
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		t.Fatalf("listRepoEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Kind != "local" || entries[0].Path != "/tmp/local-repo" {
		t.Fatalf("entry = %#v, want local /tmp/local-repo", entries[0])
	}
}

func TestFinalizeWorktreeSetupRunsMiseWhenConfigPresent(t *testing.T) {
	worktreePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktreePath, "mise.toml"), []byte("[tools]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	restore := stubExecCommand(t)
	defer restore()

	if err := finalizeWorktreeSetup(worktreePath, Repo{}); err != nil {
		t.Fatalf("finalizeWorktreeSetup() error = %v", err)
	}

	commands := readCommandLog(t)
	want := []string{
		"git\tsubmodule\tupdate\t--init\t--recursive",
		"mise\ttrust\t" + filepath.Join(worktreePath, "mise.toml"),
		"mise\tinstall",
	}
	if len(commands) != len(want) {
		t.Fatalf("len(commands) = %d, want %d (%v)", len(commands), len(want), commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("command %d = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestFinalizeWorktreeSetupSkipsMiseWithoutConfig(t *testing.T) {
	worktreePath := t.TempDir()

	restore := stubExecCommand(t)
	defer restore()

	if err := finalizeWorktreeSetup(worktreePath, Repo{}); err != nil {
		t.Fatalf("finalizeWorktreeSetup() error = %v", err)
	}

	commands := readCommandLog(t)
	want := []string{
		"git\tsubmodule\tupdate\t--init\t--recursive",
	}
	if len(commands) != len(want) {
		t.Fatalf("len(commands) = %d, want %d (%v)", len(commands), len(want), commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("command %d = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestFinalizeWorktreeSetupSetsGitHubDefaultRepoWhenGHPresent(t *testing.T) {
	worktreePath := t.TempDir()
	repo := Repo{Owner: "owner", Name: "repo"}

	restoreExec := stubExecCommand(t)
	defer restoreExec()
	restoreLookPath := stubExecLookPath(t, func(name string) (string, error) {
		if name == "gh" {
			return "/usr/bin/gh", nil
		}
		return "", exec.ErrNotFound
	})
	defer restoreLookPath()

	if err := finalizeWorktreeSetup(worktreePath, repo); err != nil {
		t.Fatalf("finalizeWorktreeSetup() error = %v", err)
	}

	commands := readCommandLog(t)
	want := []string{
		"git\tsubmodule\tupdate\t--init\t--recursive",
		"gh\trepo\tset-default\towner/repo",
	}
	if len(commands) != len(want) {
		t.Fatalf("len(commands) = %d, want %d (%v)", len(commands), len(want), commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("command %d = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestFinalizeWorktreeSetupGitHubDefaultRepoFailure(t *testing.T) {
	worktreePath := t.TempDir()
	repo := Repo{Owner: "owner", Name: "repo"}

	restoreLookPath := stubExecLookPath(t, func(name string) (string, error) {
		if name == "gh" {
			return "/usr/bin/gh", nil
		}
		return "", exec.ErrNotFound
	})
	defer restoreLookPath()

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		if name == "gh" {
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		}
		cmd.Env = env
		return cmd
	}

	err := finalizeWorktreeSetup(worktreePath, repo)
	if err == nil {
		t.Fatal("finalizeWorktreeSetup() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "set gh default repo for owner/repo") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "set gh default repo for owner/repo")
	}
}

func TestParsePRNumberTable(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		want   int
		wantOk bool
	}{
		{name: "empty", input: "", want: 0, wantOk: false},
		{name: "hash only", input: "#", want: 0, wantOk: false},
		{name: "non-digit", input: "abc", want: 0, wantOk: false},
		{name: "zero", input: "0", want: 0, wantOk: false},
		{name: "negative", input: "-1", want: 0, wantOk: false},
		{name: "hash with trailing space", input: "#42 ", want: 42, wantOk: true},
		{name: "leading zero", input: "007", want: 7, wantOk: true},
		{name: "plain digits", input: "99", want: 99, wantOk: true},
		{name: "hash digits", input: "#42", want: 42, wantOk: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotOk := parsePRNumber(tc.input)
			if got != tc.want || gotOk != tc.wantOk {
				t.Fatalf("parsePRNumber(%q) = (%d, %t), want (%d, %t)", tc.input, got, gotOk, tc.want, tc.wantOk)
			}
		})
	}
}

func TestBranchNameFromWorktreeTable(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "with slash", input: "feature/x", want: "feature/x"},
		{name: "trims whitespace", input: " feature ", want: "feature"},
		{name: "removes dot prefix", input: "./foo", want: "foo"},
		{name: "single segment", input: "foo", want: "foo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := branchNameFromWorktree(tc.input)
			if got != tc.want {
				t.Fatalf("branchNameFromWorktree(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCleanRelativePathTable(t *testing.T) {
	cases := []struct {
		name          string
		input         string
		want          string
		wantErrSubstr string
	}{
		{name: "empty", input: "", wantErrSubstr: "path name cannot be empty"},
		{name: "whitespace only", input: "   ", wantErrSubstr: "path name cannot be empty"},
		{name: "absolute", input: "/etc", wantErrSubstr: "path name must be relative"},
		{name: "dot dot", input: "..", wantErrSubstr: "must not escape the repository"},
		{name: "escape", input: "../x", wantErrSubstr: "must not escape the repository"},
		{name: "valid nested", input: "a/b/c", want: filepath.Join("a", "b", "c")},
		{name: "single segment", input: "foo", want: "foo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanRelativePath(tc.input)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("cleanRelativePath(%q) error = nil, want substring %q", tc.input, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("cleanRelativePath(%q) error = %q, want substring %q", tc.input, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("cleanRelativePath(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("cleanRelativePath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSplitRepoSpecTable(t *testing.T) {
	cases := []struct {
		name          string
		input         string
		wantOwner     string
		wantRepo      string
		wantErrSubstr string
	}{
		{name: "valid", input: "owner/repo", wantOwner: "owner", wantRepo: "repo"},
		{name: "trims slashes", input: "/owner/repo/", wantOwner: "owner", wantRepo: "repo"},
		{name: "no slash", input: "foo", wantErrSubstr: "must be in the form owner/repo"},
		{name: "too many slashes", input: "a/b/c", wantErrSubstr: "must be in the form owner/repo"},
		{name: "empty owner", input: "/repo", wantErrSubstr: "must be in the form owner/repo"},
		{name: "empty repo", input: "owner/", wantErrSubstr: "must be in the form owner/repo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := splitRepoSpec(tc.input)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("splitRepoSpec(%q) error = nil, want substring %q", tc.input, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("splitRepoSpec(%q) error = %q, want substring %q", tc.input, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitRepoSpec(%q) error = %v", tc.input, err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo {
				t.Fatalf("splitRepoSpec(%q) = (%q, %q), want (%q, %q)", tc.input, owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}

func TestParseStatusArgsTable(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		wantFiles     bool
		wantRepoArgs  []string
		wantErrSubstr string
	}{
		{name: "long files flag", args: []string{"--files", "owner/repo"}, wantFiles: true, wantRepoArgs: []string{"owner/repo"}},
		{name: "short f flag", args: []string{"-f", "owner/repo"}, wantFiles: true, wantRepoArgs: []string{"owner/repo"}},
		{name: "separator with repo", args: []string{"--", "owner/repo"}, wantFiles: false, wantRepoArgs: []string{"owner/repo"}},
		{name: "files separator combo", args: []string{"--files", "--", "owner/repo"}, wantFiles: true, wantRepoArgs: []string{"owner/repo"}},
		{name: "no repo args", args: []string{"--files"}, wantErrSubstr: "usage: gg status"},
		{name: "unknown flag", args: []string{"--nope"}, wantErrSubstr: "unknown status flag"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options, repoArgs, err := parseStatusArgs(tc.args)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("parseStatusArgs(%v) error = nil, want substring %q", tc.args, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("parseStatusArgs(%v) error = %q, want substring %q", tc.args, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStatusArgs(%v) error = %v", tc.args, err)
			}
			if options.ShowFiles != tc.wantFiles {
				t.Fatalf("parseStatusArgs(%v) ShowFiles = %t, want %t", tc.args, options.ShowFiles, tc.wantFiles)
			}
			if len(repoArgs) != len(tc.wantRepoArgs) {
				t.Fatalf("parseStatusArgs(%v) repoArgs = %v, want %v", tc.args, repoArgs, tc.wantRepoArgs)
			}
			for i := range tc.wantRepoArgs {
				if repoArgs[i] != tc.wantRepoArgs[i] {
					t.Fatalf("parseStatusArgs(%v) repoArgs[%d] = %q, want %q", tc.args, i, repoArgs[i], tc.wantRepoArgs[i])
				}
			}
		})
	}
}

func TestParseRepoStatusTable(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantBranch string
		wantFiles  []string
	}{
		{name: "empty output", input: "", wantBranch: "", wantFiles: nil},
		{name: "branch only", input: "## main\n", wantBranch: "main", wantFiles: nil},
		{name: "files no branch", input: " M repo.go\n?? new.txt\n", wantBranch: "", wantFiles: []string{"M repo.go", "?? new.txt"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRepoStatus(tc.input)
			if got.Branch != tc.wantBranch {
				t.Fatalf("parseRepoStatus(%q) Branch = %q, want %q", tc.input, got.Branch, tc.wantBranch)
			}
			if len(got.Files) != len(tc.wantFiles) {
				t.Fatalf("parseRepoStatus(%q) Files = %v, want %v", tc.input, got.Files, tc.wantFiles)
			}
			for i := range tc.wantFiles {
				if got.Files[i] != tc.wantFiles[i] {
					t.Fatalf("parseRepoStatus(%q) Files[%d] = %q, want %q", tc.input, i, got.Files[i], tc.wantFiles[i])
				}
			}
		})
	}
}

func TestExpandAliasTable(t *testing.T) {
	aliases := map[string]string{
		"a":     "b",
		"b":     "  ",
		"loop1": "loop2",
		"loop2": "loop1",
		"slash": "owner/repo",
		"ok":    "ForkbombEu",
		" pad ": "padded",
	}

	cases := []struct {
		name          string
		key           string
		allowSlash    bool
		want          string
		wantErrSubstr string
	}{
		{name: "empty input", key: "", allowSlash: true, wantErrSubstr: "empty repository alias"},
		{name: "chain to empty", key: "a", allowSlash: true, wantErrSubstr: "resolves to an empty value"},
		{name: "alias cycle", key: "loop1", allowSlash: true, wantErrSubstr: "alias cycle detected"},
		{name: "slash disallowed direct", key: "owner/repo", allowSlash: false, wantErrSubstr: "expects a single path segment"},
		{name: "slash disallowed via alias", key: "slash", allowSlash: false, wantErrSubstr: "expects a single path segment"},
		{name: "slash allowed", key: "slash", allowSlash: true, want: "owner/repo"},
		{name: "trimmed key matches", key: " ok ", allowSlash: false, want: "ForkbombEu"},
		{name: "no alias passthrough", key: "untouched", allowSlash: false, want: "untouched"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandAlias(aliases, tc.key, tc.allowSlash)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expandAlias(%q) error = nil, want substring %q", tc.key, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("expandAlias(%q) error = %q, want substring %q", tc.key, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandAlias(%q) error = %v", tc.key, err)
			}
			if got != tc.want {
				t.Fatalf("expandAlias(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestCanonicalizeSegment(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "ForkbombEu"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	t.Run("matches existing dir case-insensitively", func(t *testing.T) {
		got := canonicalizeSegment(parent, "forkbombeu")
		if got != "ForkbombEu" {
			t.Fatalf("canonicalizeSegment = %q, want %q", got, "ForkbombEu")
		}
	})

	t.Run("returns input when parent missing", func(t *testing.T) {
		got := canonicalizeSegment(filepath.Join(parent, "nope"), "Hello")
		if got != "Hello" {
			t.Fatalf("canonicalizeSegment = %q, want %q", got, "Hello")
		}
	})

	t.Run("empty name returns empty", func(t *testing.T) {
		got := canonicalizeSegment(parent, "")
		if got != "" {
			t.Fatalf("canonicalizeSegment = %q, want %q", got, "")
		}
	})

	t.Run("trims and falls back when no match", func(t *testing.T) {
		got := canonicalizeSegment(parent, " unknown ")
		if got != "unknown" {
			t.Fatalf("canonicalizeSegment = %q, want %q", got, "unknown")
		}
	})

	t.Run("skips non-directory siblings", func(t *testing.T) {
		mixedParent := t.TempDir()
		if err := os.WriteFile(filepath.Join(mixedParent, "README"), []byte("readme\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := os.MkdirAll(filepath.Join(mixedParent, "Real"), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		got := canonicalizeSegment(mixedParent, "readme")
		if got != "readme" {
			t.Fatalf("canonicalizeSegment(file sibling) = %q, want %q", got, "readme")
		}
		gotMatch := canonicalizeSegment(mixedParent, "real")
		if gotMatch != "Real" {
			t.Fatalf("canonicalizeSegment(dir sibling) = %q, want %q", gotMatch, "Real")
		}
	})
}

func setupTestConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", root)
	cfg := Config{
		Root: filepath.Join(root, "src"),
		Host: "github.com",
	}
	return cfg
}

func TestRunHelpVariants(t *testing.T) {
	cases := []string{"help", "-h", "--help"}
	for _, arg := range cases {
		t.Run(arg, func(t *testing.T) {
			output, err := captureStdout(t, func() error {
				return run([]string{arg})
			})
			if err != nil {
				t.Fatalf("run(%q) error = %v", arg, err)
			}
			for _, want := range []string{"alias", "new", "list", "status", "starship", "prune", "shell-init"} {
				if !strings.Contains(output, want) {
					t.Fatalf("run(%q) usage missing %q in:\n%s", arg, want, output)
				}
			}
		})
	}
}

func TestRunVersionVariants(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		t.Run(arg, func(t *testing.T) {
			output, err := captureStdout(t, func() error {
				return run([]string{arg})
			})
			if err != nil {
				t.Fatalf("run(%q) error = %v", arg, err)
			}
			got := strings.TrimSpace(output)
			if got != version {
				t.Fatalf("run(%q) output = %q, want %q", arg, got, version)
			}
		})
	}
}

func TestRunConfigPath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)

	output, err := captureStdout(t, func() error {
		return run([]string{"config-path"})
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	got := strings.TrimSpace(output)
	if got != configPath {
		t.Fatalf("config-path output = %q, want %q", got, configPath)
	}
}

func TestNewCommandCreatesRepositoryFromMarkdownTemplates(t *testing.T) {
	cfg := setupTestConfig(t)

	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "git" && len(args) == 5 && args[0] == "clone" && args[4] != "" {
			templateRoot := args[4]
			if err := os.MkdirAll(filepath.Join(templateRoot, "nested"), 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(templateRoot, "README.md"), []byte("# Template\n"), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(templateRoot, "nested", "NOTES.md"), []byte("notes\n"), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(templateRoot, "ignore.txt"), []byte("ignore\n"), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
		}

		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}

	output, err := captureStdout(t, func() error {
		return newCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("newCommand() error = %v", err)
	}

	repoPath := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	if strings.TrimSpace(output) != repoPath {
		t.Fatalf("output = %q, want %q", strings.TrimSpace(output), repoPath)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "README.md")); err != nil {
		t.Fatalf("README.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "nested", "NOTES.md")); err != nil {
		t.Fatalf("nested NOTES.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "ignore.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignore.txt exists or unexpected stat error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	log := string(logData)
	for _, want := range []string{
		"git\tclone\t--depth\t1\thttps://github.com/puria/md.git",
		"git\tinit",
		"git\tadd\t--all",
		"git\tcommit\t-m\t" + initialCommitMsg,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("command log missing %q in:\n%s", want, log)
		}
	}
}

func TestRunNewCommand(t *testing.T) {
	cfg := setupTestConfig(t)
	defer stubNewCommandGit(t, "", true)()

	output, err := captureStdout(t, func() error {
		return run([]string{"new", "owner/repo"})
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}

	repoPath := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	if strings.TrimSpace(output) != repoPath {
		t.Fatalf("output = %q, want %q", strings.TrimSpace(output), repoPath)
	}
}

func TestNewCommandRejectsExistingRepository(t *testing.T) {
	cfg := setupTestConfig(t)
	existing := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	err := newCommand([]string{"owner/repo"})
	if err == nil {
		t.Fatal("newCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "repository already exists") {
		t.Fatalf("error = %q, want repository already exists", err.Error())
	}
}

func TestNewCommandRejectsInvalidArgs(t *testing.T) {
	err := newCommand(nil)
	if err == nil {
		t.Fatal("newCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "usage: gg new") {
		t.Fatalf("error = %q, want usage", err.Error())
	}
}

func TestNewCommandRejectsInvalidRepoSpec(t *testing.T) {
	setupTestConfig(t)

	err := newCommand([]string{"owner/repo/extra"})
	if err == nil {
		t.Fatal("newCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "repository must be in the form owner/repo") {
		t.Fatalf("error = %q, want repository form error", err.Error())
	}
}

func TestNewCommandPropagatesConfigLoadFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(configPath, []byte("{"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := newCommand([]string{"owner/repo"})
	if err == nil {
		t.Fatal("newCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("error = %q, want parse config", err.Error())
	}
}

func TestNewCommandFilesystemFailures(t *testing.T) {
	t.Run("stat", func(t *testing.T) {
		setupTestConfig(t)

		oldStat := osStat
		defer func() { osStat = oldStat }()
		osStat = func(string) (os.FileInfo, error) {
			return nil, errors.New("boom")
		}

		err := newCommand([]string{"owner/repo"})
		if err == nil {
			t.Fatal("newCommand() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "stat") {
			t.Fatalf("error = %q, want stat error", err.Error())
		}
	})

	t.Run("mkdir repo", func(t *testing.T) {
		setupTestConfig(t)

		oldMkdirAll := osMkdirAll
		defer func() { osMkdirAll = oldMkdirAll }()
		osMkdirAll = func(string, os.FileMode) error {
			return errors.New("boom")
		}

		err := newCommand([]string{"owner/repo"})
		if err == nil {
			t.Fatal("newCommand() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "create repository directory") {
			t.Fatalf("error = %q, want create repository directory", err.Error())
		}
	})

	t.Run("mkdir temp", func(t *testing.T) {
		setupTestConfig(t)

		oldMkdirTemp := osMkdirTemp
		defer func() { osMkdirTemp = oldMkdirTemp }()
		osMkdirTemp = func(string, string) (string, error) {
			return "", errors.New("boom")
		}

		err := newCommand([]string{"owner/repo"})
		if err == nil {
			t.Fatal("newCommand() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "create template download directory") {
			t.Fatalf("error = %q, want template download directory", err.Error())
		}
	})
}

func TestNewCommandGitAndTemplateFailures(t *testing.T) {
	cases := []struct {
		name    string
		failArg string
		want    string
	}{
		{name: "clone", failArg: "clone", want: "download markdown templates"},
		{name: "init", failArg: "init", want: "initialize git repository"},
		{name: "add", failArg: "add", want: "stage initial files"},
		{name: "commit", failArg: "commit", want: "create initial commit"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupTestConfig(t)
			defer stubNewCommandGit(t, tc.failArg, true)()

			err := newCommand([]string{"owner/repo"})
			if err == nil {
				t.Fatal("newCommand() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}

	t.Run("no markdown", func(t *testing.T) {
		setupTestConfig(t)
		defer stubNewCommandGit(t, "", false)()

		err := newCommand([]string{"owner/repo"})
		if err == nil {
			t.Fatal("newCommand() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "no markdown files found") {
			t.Fatalf("error = %q, want no markdown files found", err.Error())
		}
	})
}

func TestCopyMarkdownFilesPropagatesWalkError(t *testing.T) {
	err := copyMarkdownFiles(filepath.Join(t.TempDir(), "missing"), t.TempDir())
	if err == nil {
		t.Fatal("copyMarkdownFiles() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "copy markdown templates") {
		t.Fatalf("error = %q, want copy markdown templates", err.Error())
	}
}

func TestCopyMarkdownFilesSkipsGitDirectory(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, ".git", "README.md"), []byte("git internals\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := copyMarkdownFiles(src, t.TempDir())
	if err == nil {
		t.Fatal("copyMarkdownFiles() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "no markdown files found") {
		t.Fatalf("error = %q, want no markdown files found", err.Error())
	}
}

func TestCopyFileFailures(t *testing.T) {
	t.Run("missing source", func(t *testing.T) {
		err := copyFile(filepath.Join(t.TempDir(), "missing.md"), filepath.Join(t.TempDir(), "README.md"))
		if err == nil {
			t.Fatal("copyFile() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "open template file") {
			t.Fatalf("error = %q, want open template file", err.Error())
		}
	})

	t.Run("mkdir", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "README.md")
		if err := os.WriteFile(src, []byte("template\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		oldMkdirAll := osMkdirAll
		defer func() { osMkdirAll = oldMkdirAll }()
		osMkdirAll = func(string, os.FileMode) error {
			return errors.New("boom")
		}

		err := copyFile(src, filepath.Join(t.TempDir(), "nested", "README.md"))
		if err == nil {
			t.Fatal("copyFile() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "create template directory") {
			t.Fatalf("error = %q, want create template directory", err.Error())
		}
	})

	t.Run("create destination", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "README.md")
		if err := os.WriteFile(src, []byte("template\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		dst := t.TempDir()

		err := copyFile(src, dst)
		if err == nil {
			t.Fatal("copyFile() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "create template file") {
			t.Fatalf("error = %q, want create template file", err.Error())
		}
	})

	t.Run("copy data", func(t *testing.T) {
		err := copyFile(t.TempDir(), filepath.Join(t.TempDir(), "README.md"))
		if err == nil {
			t.Fatal("copyFile() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "copy template file") {
			t.Fatalf("error = %q, want copy template file", err.Error())
		}
	})
}

func TestRunShellInitVariants(t *testing.T) {
	defer withStubExecutable(t, "/tmp/bin/gg")()

	cases := []struct {
		name           string
		args           []string
		wantSubstr     string
		wantErrSubstr  string
		wantExitInBody string
	}{
		{name: "fish", args: []string{"shell-init", "fish"}, wantSubstr: `set -l gg_bin "/tmp/bin/gg"`},
		{name: "bash", args: []string{"shell-init", "bash"}, wantSubstr: `local gg_bin="/tmp/bin/gg"`},
		{name: "zsh", args: []string{"shell-init", "zsh"}, wantSubstr: `local gg_bin="/tmp/bin/gg"`},
		{name: "no shell arg", args: []string{"shell-init"}, wantErrSubstr: "usage: gg shell-init"},
		{name: "unknown shell", args: []string{"shell-init", "tcsh"}, wantErrSubstr: "unsupported shell"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, err := captureStdout(t, func() error {
				return run(tc.args)
			})
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("run(%v) error = nil, want substring %q", tc.args, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("run(%v) error = %q, want substring %q", tc.args, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("run(%v) error = %v", tc.args, err)
			}
			if !strings.Contains(output, tc.wantSubstr) {
				t.Fatalf("run(%v) output missing %q in:\n%s", tc.args, tc.wantSubstr, output)
			}
		})
	}
}

func TestRunPathDispatch(t *testing.T) {
	cfg := setupTestConfig(t)

	containerPath := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	mainPath := filepath.Join(containerPath, "main")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(containerPath, ".bare"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	t.Run("default branch resolves args", func(t *testing.T) {
		output, err := captureStdout(t, func() error {
			return run([]string{"owner/repo"})
		})
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
		if strings.TrimSpace(output) != mainPath {
			t.Fatalf("output = %q, want %q", strings.TrimSpace(output), mainPath)
		}
	})

	t.Run("explicit path subcommand", func(t *testing.T) {
		output, err := captureStdout(t, func() error {
			return run([]string{"path", "owner/repo"})
		})
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
		if strings.TrimSpace(output) != mainPath {
			t.Fatalf("output = %q, want %q", strings.TrimSpace(output), mainPath)
		}
	})

	t.Run("no args produces usage error", func(t *testing.T) {
		_, err := captureStdout(t, func() error {
			return run(nil)
		})
		if err == nil {
			t.Fatal("run(nil) error = nil, want usage error")
		}
		if !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("run(nil) error = %q, want substring %q", err.Error(), "usage:")
		}
	})
}

func TestInitConfigCommand(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "gg", "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())

	output, err := captureStdout(t, func() error {
		return initConfigCommand()
	})
	if err != nil {
		t.Fatalf("initConfigCommand() error = %v", err)
	}
	if strings.TrimSpace(output) != configPath {
		t.Fatalf("output = %q, want %q", strings.TrimSpace(output), configPath)
	}

	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	cfg, gotPath, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if gotPath != configPath {
		t.Fatalf("loadConfig path = %q, want %q", gotPath, configPath)
	}
	if cfg.Host != "github.com" {
		t.Fatalf("cfg.Host = %q, want github.com", cfg.Host)
	}

	t.Run("rejects existing config", func(t *testing.T) {
		_, err := captureStdout(t, func() error {
			return initConfigCommand()
		})
		if err == nil {
			t.Fatal("initConfigCommand() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "config already exists") {
			t.Fatalf("error = %q, want substring %q", err.Error(), "config already exists")
		}
	})
}

func TestAliasCommandLists(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())

	if err := os.WriteFile(configPath, []byte(`{"aliases":{"zeta":"z","alpha":"a"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	output, err := captureStdout(t, func() error {
		return aliasCommand(nil)
	})
	if err != nil {
		t.Fatalf("aliasCommand() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 (%v)", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "alpha") || !strings.HasSuffix(lines[0], "-> a") {
		t.Fatalf("line[0] = %q, want alpha first sorted entry", lines[0])
	}
	if !strings.HasPrefix(lines[1], "zeta") || !strings.HasSuffix(lines[1], "-> z") {
		t.Fatalf("line[1] = %q, want zeta second sorted entry", lines[1])
	}
}

func TestAliasCommandUsageErrors(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())

	cases := []struct {
		name string
		args []string
	}{
		{name: "single arg", args: []string{"only"}},
		{name: "empty target", args: []string{"   ", "name"}},
		{name: "empty name", args: []string{"target", "   "}},
		{name: "too many args", args: []string{"a", "b", "c"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := aliasCommand(tc.args)
			if err == nil {
				t.Fatalf("aliasCommand(%v) error = nil, want usage error", tc.args)
			}
			if !strings.Contains(err.Error(), "usage: gg alias") {
				t.Fatalf("aliasCommand(%v) error = %q, want substring %q", tc.args, err.Error(), "usage: gg alias")
			}
		})
	}
}

func TestResolveRepoArgsTable(t *testing.T) {
	cfg := Config{
		Root: t.TempDir(),
		Host: "github.com",
		Aliases: map[string]string{
			"f":      "ForkbombEu",
			"credim": "credimi",
		},
	}

	cases := []struct {
		name      string
		args      []string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{name: "one arg owner/repo", args: []string{"ForkbombEu/credimi"}, wantOwner: "ForkbombEu", wantRepo: "credimi"},
		{name: "two args", args: []string{"f", "credim"}, wantOwner: "ForkbombEu", wantRepo: "credimi"},
		{name: "zero args", args: nil, wantErr: true},
		{name: "three args", args: []string{"a", "b", "c"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := resolveRepoArgs(cfg, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveRepoArgs(%v) error = nil, want error", tc.args)
				}
				if !strings.Contains(err.Error(), "usage: gg <command>") {
					t.Fatalf("resolveRepoArgs(%v) error = %q, want substring %q", tc.args, err.Error(), "usage: gg <command>")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRepoArgs(%v) error = %v", tc.args, err)
			}
			if repo.Owner != tc.wantOwner || repo.Name != tc.wantRepo {
				t.Fatalf("resolveRepoArgs(%v) = %s/%s, want %s/%s", tc.args, repo.Owner, repo.Name, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}

func TestFindRepoStore(t *testing.T) {
	cfg := Config{
		Root: t.TempDir(),
		Host: "github.com",
	}

	t.Run("missing container errors", func(t *testing.T) {
		_, err := findRepoStore(cfg, Repo{Owner: "missing", Name: "repo"})
		if err == nil {
			t.Fatal("findRepoStore() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "not available locally") {
			t.Fatalf("error = %q, want substring %q", err.Error(), "not available locally")
		}
	})

	t.Run("managed classification", func(t *testing.T) {
		repo := Repo{Owner: "owner", Name: "managed"}
		container := repo.ContainerPath(cfg)
		if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		store, err := findRepoStore(cfg, repo)
		if err != nil {
			t.Fatalf("findRepoStore() error = %v", err)
		}
		if !store.Managed {
			t.Fatalf("store.Managed = false, want true")
		}
		if store.MainPath != filepath.Join(container, "main") {
			t.Fatalf("store.MainPath = %q, want %q", store.MainPath, filepath.Join(container, "main"))
		}
		if store.GitDir != filepath.Join(container, ".bare") {
			t.Fatalf("store.GitDir = %q, want %q", store.GitDir, filepath.Join(container, ".bare"))
		}
	})

	t.Run("local classification", func(t *testing.T) {
		repo := Repo{Owner: "owner", Name: "local"}
		container := repo.ContainerPath(cfg)
		if err := os.MkdirAll(container, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(container, "README.md"), []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		store, err := findRepoStore(cfg, repo)
		if err != nil {
			t.Fatalf("findRepoStore() error = %v", err)
		}
		if store.Managed {
			t.Fatal("store.Managed = true, want false")
		}
		if store.MainPath != container {
			t.Fatalf("store.MainPath = %q, want %q", store.MainPath, container)
		}
		if store.GitDir != "" {
			t.Fatalf("store.GitDir = %q, want empty", store.GitDir)
		}
	})
}

func TestListCommand(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	for _, dir := range []string{
		filepath.Join(container, ".bare"),
		filepath.Join(container, "main"),
		filepath.Join(container, "worktrees", "feature-x"),
		filepath.Join(container, "PR", "99"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	for _, sentinel := range []string{
		filepath.Join(container, "main", ".git"),
		filepath.Join(container, "worktrees", "feature-x", ".git"),
		filepath.Join(container, "PR", "99", ".git"),
	} {
		if err := os.WriteFile(sentinel, []byte("gitdir: x"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	output, err := captureStdout(t, func() error {
		return listCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("listCommand() error = %v", err)
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (%v)", len(lines), lines)
	}

	mainLine := fmt.Sprintf("%-8s %-20s %s", "main", "main", filepath.Join(container, "main"))
	prLine := fmt.Sprintf("%-8s %-20s %s", "pr", "99", filepath.Join(container, "PR", "99"))
	worktreeLine := fmt.Sprintf("%-8s %-20s %s", "worktree", "feature-x", filepath.Join(container, "worktrees", "feature-x"))

	if lines[0] != mainLine {
		t.Fatalf("lines[0] = %q, want %q", lines[0], mainLine)
	}
	if lines[1] != prLine {
		t.Fatalf("lines[1] = %q, want %q", lines[1], prLine)
	}
	if lines[2] != worktreeLine {
		t.Fatalf("lines[2] = %q, want %q", lines[2], worktreeLine)
	}
}

func TestListCommandLocal(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "local")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(container, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	output, err := captureStdout(t, func() error {
		return listCommand([]string{"owner/local"})
	})
	if err != nil {
		t.Fatalf("listCommand() error = %v", err)
	}
	if !strings.Contains(output, "local") || !strings.Contains(output, container) {
		t.Fatalf("listCommand output missing local entry in:\n%s", output)
	}
}

func TestStatusCommandClean(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	mainPath := filepath.Join(container, "main")
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainPath, ".git"), []byte("gitdir: x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	defer stubExecCommand(t)()

	output, err := captureStdout(t, func() error {
		return statusCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("statusCommand() error = %v", err)
	}
	if !strings.Contains(output, "status  clean") {
		t.Fatalf("status output missing clean marker:\n%s", output)
	}

	commands := readCommandLog(t)
	want := "git\tstatus\t--porcelain=v1\t--branch"
	if len(commands) != 1 || commands[0] != want {
		t.Fatalf("commands = %v, want exactly [%q]", commands, want)
	}
}

func TestStatusCommandDirtyWithFiles(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	mainPath := filepath.Join(container, "main")
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainPath, ".git"), []byte("gitdir: x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_STDOUT", "## main\n M repo.go\n?? new.txt\n")

	output, err := captureStdout(t, func() error {
		return statusCommand([]string{"--files", "owner/repo"})
	})
	if err != nil {
		t.Fatalf("statusCommand() error = %v", err)
	}
	for _, want := range []string{"branch  main", "status  dirty (2 changes)", "M repo.go", "?? new.txt"} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q in:\n%s", want, output)
		}
	}
}

func TestStatusCommandDirtyWithoutFiles(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	mainPath := filepath.Join(container, "main")
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainPath, ".git"), []byte("gitdir: x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_STDOUT", "## main\n M repo.go\n")

	output, err := captureStdout(t, func() error {
		return statusCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("statusCommand() error = %v", err)
	}
	if !strings.Contains(output, "status  dirty (1 change)") {
		t.Fatalf("status output missing single-change marker:\n%s", output)
	}
	if strings.Contains(output, "M repo.go") {
		t.Fatalf("status output should NOT include file list without --files:\n%s", output)
	}
}

func TestStatusCommandMissingRepo(t *testing.T) {
	setupTestConfig(t)

	err := statusCommand([]string{"missing/repo"})
	if err == nil {
		t.Fatal("statusCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "not available locally") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "not available locally")
	}
}

func TestStarshipCommandWorktree(t *testing.T) {
	cfg := setupTestConfig(t)

	path := filepath.Join(cfg.Root, cfg.Host, "owner", "repo", "worktrees", "feature-x", "pkg")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Chdir(path)

	output, err := captureStdout(t, func() error {
		return starshipCommand([]string{"worktree"})
	})
	if err != nil {
		t.Fatalf("starshipCommand() error = %v", err)
	}
	if strings.TrimSpace(output) != "feature-x" {
		t.Fatalf("output = %q, want feature-x", strings.TrimSpace(output))
	}

	output, err = captureStdout(t, func() error {
		return starshipCommand(nil)
	})
	if err != nil {
		t.Fatalf("starshipCommand() error = %v", err)
	}
	if strings.TrimSpace(output) != "owner/repo wt:feature-x" {
		t.Fatalf("summary = %q, want owner/repo wt:feature-x", strings.TrimSpace(output))
	}
}

func TestStarshipCommandPR(t *testing.T) {
	cfg := setupTestConfig(t)

	path := filepath.Join(cfg.Root, cfg.Host, "owner", "repo", "PR", "99", "pkg")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Chdir(path)

	output, err := captureStdout(t, func() error {
		return starshipCommand([]string{"pr"})
	})
	if err != nil {
		t.Fatalf("starshipCommand() error = %v", err)
	}
	if strings.TrimSpace(output) != "#99" {
		t.Fatalf("output = %q, want #99", strings.TrimSpace(output))
	}

	output, err = captureStdout(t, func() error {
		return starshipCommand([]string{"worktree"})
	})
	if !errors.Is(err, errSilent) {
		t.Fatalf("starshipCommand() error = %v, want silent exit", err)
	}
	if strings.TrimSpace(output) != "" {
		t.Fatalf("worktree output = %q, want empty for PR", strings.TrimSpace(output))
	}
}

func TestStarshipCommandOutsideRootIsQuiet(t *testing.T) {
	setupTestConfig(t)
	t.Chdir(t.TempDir())

	output, err := captureStdout(t, func() error {
		return starshipCommand(nil)
	})
	if !errors.Is(err, errSilent) {
		t.Fatalf("starshipCommand() error = %v, want silent exit", err)
	}
	if strings.TrimSpace(output) != "" {
		t.Fatalf("output = %q, want empty outside gg root", strings.TrimSpace(output))
	}
}

func TestStarshipCommandInvalidPart(t *testing.T) {
	setupTestConfig(t)

	err := starshipCommand([]string{"nope"})
	if err == nil {
		t.Fatal("starshipCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "usage: gg starship") {
		t.Fatalf("error = %q, want usage", err.Error())
	}
}

func TestPruneCommandNothingToPrune(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	defer stubExecCommand(t)()

	output, err := captureStdout(t, func() error {
		return pruneCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("pruneCommand() error = %v", err)
	}
	if !strings.Contains(output, "nothing to prune") {
		t.Fatalf("output missing 'nothing to prune':\n%s", output)
	}

	commands := readCommandLog(t)
	want := "git\t--git-dir\t" + filepath.Join(container, ".bare") + "\tworktree\tprune\t--verbose"
	if len(commands) != 1 || commands[0] != want {
		t.Fatalf("commands = %v, want exactly [%q]", commands, want)
	}
}

func TestPruneCommandRemovesEmptyChildren(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	emptyWorktree := filepath.Join(container, "worktrees", "feature-x")
	emptyPR := filepath.Join(container, "PR", "99")
	for _, dir := range []string{
		filepath.Join(container, ".bare"),
		emptyWorktree,
		emptyPR,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}

	defer stubExecCommand(t)()

	output, err := captureStdout(t, func() error {
		return pruneCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("pruneCommand() error = %v", err)
	}
	for _, want := range []string{
		"removed empty directory " + emptyWorktree,
		"removed empty directory " + emptyPR,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q in:\n%s", want, output)
		}
	}
	if _, err := os.Stat(emptyWorktree); !os.IsNotExist(err) {
		t.Fatalf("empty worktree still exists: %v", err)
	}
	if _, err := os.Stat(emptyPR); !os.IsNotExist(err) {
		t.Fatalf("empty PR still exists: %v", err)
	}
}

func TestPruneCommandRemovesMergedWorktreeAndPR(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	gitDir, _ := seedBareRepoAt(t, container)
	worktreePath := filepath.Join(container, "worktrees", "feature")
	prPath := filepath.Join(container, "PR", "7")

	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "-b", "feature", worktreePath, "main")
	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "--detach", prPath, "main")

	output, err := captureStdout(t, func() error {
		return pruneCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("pruneCommand() error = %v", err)
	}

	for _, want := range []string{
		"removed merged worktree feature " + worktreePath,
		"removed merged pr 7 " + prPath,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q in:\n%s", want, output)
		}
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("merged worktree still exists: %v", err)
	}
	if _, err := os.Stat(prPath); !os.IsNotExist(err) {
		t.Fatalf("merged PR still exists: %v", err)
	}
	if err := exec.Command("git", "--git-dir", gitDir, "rev-parse", "--verify", "--quiet", "refs/heads/feature").Run(); err == nil {
		t.Fatal("feature branch still exists after merged worktree prune")
	}
}

func TestPruneCommandKeepsUnmergedWorktree(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	gitDir, _ := seedBareRepoAt(t, container)
	worktreePath := filepath.Join(container, "worktrees", "feature")

	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "-b", "feature", worktreePath, "main")
	if err := os.WriteFile(filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runGit(t, worktreePath, "add", "feature.txt")
	runGit(t, worktreePath, "commit", "-m", "feature")

	output, err := captureStdout(t, func() error {
		return pruneCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("pruneCommand() error = %v", err)
	}

	if strings.Contains(output, "removed merged worktree") {
		t.Fatalf("output should not report removing unmerged worktree:\n%s", output)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("unmerged worktree missing: %v", err)
	}
}

func TestPruneCommandSkipsDirtyWorktree(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	gitDir, _ := seedBareRepoAt(t, container)
	worktreePath := filepath.Join(container, "worktrees", "dirty")

	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "-b", "dirty", worktreePath, "main")
	if err := os.WriteFile(filepath.Join(worktreePath, "scratch.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	output, err := captureStdout(t, func() error {
		return pruneCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("pruneCommand() error = %v", err)
	}

	if !strings.Contains(output, "skipped dirty worktree dirty "+worktreePath) {
		t.Fatalf("output missing dirty skip in:\n%s", output)
	}
	if strings.Contains(output, "removed merged worktree") {
		t.Fatalf("output should not report removing dirty worktree:\n%s", output)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("dirty worktree missing: %v", err)
	}
}

func TestPruneCommandRejectsLocalRepo(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "local")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(container, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := pruneCommand([]string{"owner/local"})
	if err == nil {
		t.Fatal("pruneCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "prune is only supported for managed repositories") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestRemoveEmptyChildrenRetainsNonEmpty(t *testing.T) {
	root := t.TempDir()
	emptyDir := filepath.Join(root, "empty")
	keptDir := filepath.Join(root, "kept")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(keptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(keptDir, "file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	removed, err := removeEmptyChildren(root)
	if err != nil {
		t.Fatalf("removeEmptyChildren() error = %v", err)
	}
	if len(removed) != 1 || removed[0] != emptyDir {
		t.Fatalf("removed = %v, want [%q]", removed, emptyDir)
	}
	if _, err := os.Stat(emptyDir); !os.IsNotExist(err) {
		t.Fatalf("empty dir still exists: %v", err)
	}
	if _, err := os.Stat(keptDir); err != nil {
		t.Fatalf("kept dir missing: %v", err)
	}
}

func TestRemoveEmptyChildrenMissingRoot(t *testing.T) {
	removed, err := removeEmptyChildren(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("removeEmptyChildren() error = %v", err)
	}
	if removed != nil {
		t.Fatalf("removed = %v, want nil for missing root", removed)
	}
}

func TestRemoveEmptyTreeNested(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	removed, err := removeEmptyTree(filepath.Join(root, "a"))
	if err != nil {
		t.Fatalf("removeEmptyTree() error = %v", err)
	}
	want := []string{deep, filepath.Join(root, "a", "b"), filepath.Join(root, "a")}
	if len(removed) != len(want) {
		t.Fatalf("removed = %v, want %v (depth-first order)", removed, want)
	}
	for i := range want {
		if removed[i] != want[i] {
			t.Fatalf("removed[%d] = %q, want %q (depth-first order)", i, removed[i], want[i])
		}
	}
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatalf("nested empty tree still exists: %v", err)
	}
}

func TestReadRepoStatusPropagatesError(t *testing.T) {
	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "5")

	_, err := readRepoStatus(t.TempDir())
	if err == nil {
		t.Fatal("readRepoStatus() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "exit status") {
		t.Fatalf("readRepoStatus() error = %q, want substring %q", err.Error(), "exit status")
	}
}

func TestLoadConfigOnlyReturnsConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	customRoot := t.TempDir()
	if err := os.WriteFile(configPath, fmt.Appendf(nil, `{"root":%q,"host":"example.com"}`, customRoot), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", "/tmp/gg-home")

	cfg, err := loadConfigOnly()
	if err != nil {
		t.Fatalf("loadConfigOnly() error = %v", err)
	}
	if cfg.Host != "example.com" {
		t.Fatalf("cfg.Host = %q, want example.com", cfg.Host)
	}
	if cfg.Root != customRoot {
		t.Fatalf("cfg.Root = %q, want %q", cfg.Root, customRoot)
	}
}

func TestCloneURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		repo Repo
		want string
	}{
		{name: "default host", cfg: Config{Host: "github.com"}, repo: Repo{Owner: "owner", Name: "repo"}, want: "https://github.com/owner/repo.git"},
		{name: "custom host", cfg: Config{Host: "gitea.example.com"}, repo: Repo{Owner: "me", Name: "x"}, want: "https://gitea.example.com/me/x.git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.repo.CloneURL(tc.cfg); got != tc.want {
				t.Fatalf("CloneURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func seedBareRepo(t *testing.T) (gitDir string, containerPath string) {
	t.Helper()
	containerPath = t.TempDir()
	gitDir = filepath.Join(containerPath, ".bare")
	mainPath := filepath.Join(containerPath, "main")

	runGit(t, "", "init", "--bare", gitDir)
	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "--orphan", "-b", "main", mainPath)
	runGit(t, mainPath, "config", "user.name", "Test User")
	runGit(t, mainPath, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(mainPath, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runGit(t, mainPath, "add", "README.md")
	runGit(t, mainPath, "commit", "-m", "seed")
	return gitDir, containerPath
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s error = %v\n%s", strings.Join(args, " "), err, out)
	}
}

func seedBareRepoAt(t *testing.T, containerPath string) (gitDir, mainPath string) {
	t.Helper()
	if err := os.MkdirAll(containerPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	gitDir = filepath.Join(containerPath, ".bare")
	mainPath = filepath.Join(containerPath, "main")
	runGit(t, "", "init", "--bare", gitDir)
	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "--orphan", "-b", "main", mainPath)
	runGit(t, mainPath, "config", "user.name", "Test User")
	runGit(t, mainPath, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(mainPath, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runGit(t, mainPath, "add", "README.md")
	runGit(t, mainPath, "commit", "-m", "seed")
	return gitDir, mainPath
}

func TestValidateBranchName(t *testing.T) {
	cases := []struct {
		name          string
		branch        string
		wantErrSubstr string
	}{
		{name: "simple", branch: "feature-x"},
		{name: "slash separator", branch: "feature/x"},
		{name: "empty", branch: "", wantErrSubstr: "branch name cannot be empty"},
		{name: "whitespace only", branch: "   ", wantErrSubstr: "branch name cannot be empty"},
		{name: "double dot", branch: "feature..x", wantErrSubstr: "invalid branch name"},
		{name: "ends with lock", branch: "feature.lock", wantErrSubstr: "invalid branch name"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBranchName(tc.branch)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("validateBranchName(%q) error = nil, want substring %q", tc.branch, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("validateBranchName(%q) error = %q, want substring %q", tc.branch, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateBranchName(%q) error = %v", tc.branch, err)
			}
		})
	}
}

func TestLocalBranchExists(t *testing.T) {
	gitDir, _ := seedBareRepo(t)

	t.Run("existing branch", func(t *testing.T) {
		exists, err := localBranchExists(gitDir, "main")
		if err != nil {
			t.Fatalf("localBranchExists() error = %v", err)
		}
		if !exists {
			t.Fatal("localBranchExists(main) = false, want true")
		}
	})

	t.Run("missing branch", func(t *testing.T) {
		exists, err := localBranchExists(gitDir, "never-existed")
		if err != nil {
			t.Fatalf("localBranchExists() error = %v", err)
		}
		if exists {
			t.Fatal("localBranchExists(never-existed) = true, want false")
		}
	})

	t.Run("bogus gitdir", func(t *testing.T) {
		_, err := localBranchExists(filepath.Join(t.TempDir(), "missing.git"), "main")
		if err == nil {
			t.Fatal("localBranchExists() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "check branch") {
			t.Fatalf("localBranchExists() error = %q, want substring %q", err.Error(), "check branch")
		}
	})
}

func TestDefaultBranchRef(t *testing.T) {
	t.Run("bare main with commits", func(t *testing.T) {
		gitDir, _ := seedBareRepo(t)
		branch, ref, err := defaultBranchRef(gitDir)
		if err != nil {
			t.Fatalf("defaultBranchRef() error = %v", err)
		}
		if branch != "main" || ref != "main" {
			t.Fatalf("defaultBranchRef() = (%q, %q), want (main, main)", branch, ref)
		}
	})

	t.Run("origin HEAD set", func(t *testing.T) {
		gitDir, _ := seedBareRepo(t)
		runGit(t, "", "--git-dir", gitDir, "update-ref", "refs/remotes/origin/main", "refs/heads/main")
		runGit(t, "", "--git-dir", gitDir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

		branch, ref, err := defaultBranchRef(gitDir)
		if err != nil {
			t.Fatalf("defaultBranchRef() error = %v", err)
		}
		if branch != "main" || ref != "origin/main" {
			t.Fatalf("defaultBranchRef() = (%q, %q), want (main, origin/main)", branch, ref)
		}
	})

	t.Run("no refs errors", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), "empty.git")
		runGit(t, "", "init", "--bare", gitDir)
		_, _, err := defaultBranchRef(gitDir)
		if err == nil {
			t.Fatal("defaultBranchRef() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "could not determine default branch") {
			t.Fatalf("defaultBranchRef() error = %q, want substring", err.Error())
		}
	})
}

func TestDefaultBaseRefMirrorsBranch(t *testing.T) {
	gitDir, _ := seedBareRepo(t)
	ref, err := defaultBaseRef(gitDir)
	if err != nil {
		t.Fatalf("defaultBaseRef() error = %v", err)
	}
	if ref != "main" {
		t.Fatalf("defaultBaseRef() = %q, want main", ref)
	}
}

func TestEnsureWorktreeRejectsUnmanaged(t *testing.T) {
	store := RepoStore{
		ContainerPath: "/tmp/local-repo",
		MainPath:      "/tmp/local-repo",
		Managed:       false,
	}
	_, err := ensureWorktree(store, "feature")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "only supported for managed repositories") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestEnsureWorktreeCreatesNewBranch(t *testing.T) {
	gitDir, container := seedBareRepo(t)
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	path, err := ensureWorktree(store, "feature/x")
	if err != nil {
		t.Fatalf("ensureWorktree() error = %v", err)
	}
	want := filepath.Join(container, "worktrees", "feature", "x")
	if path != want {
		t.Fatalf("ensureWorktree() = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree directory missing: %v", err)
	}
	cmd := exec.Command("git", "--git-dir", gitDir, "rev-parse", "--verify", "--quiet", "refs/heads/feature/x")
	if err := cmd.Run(); err != nil {
		t.Fatalf("expected branch feature/x created: %v", err)
	}
}

func TestEnsureWorktreeReusesExistingBranch(t *testing.T) {
	gitDir, container := seedBareRepo(t)
	runGit(t, "", "--git-dir", gitDir, "branch", "existing", "main")

	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}
	path, err := ensureWorktree(store, "existing")
	if err != nil {
		t.Fatalf("ensureWorktree() error = %v", err)
	}
	want := filepath.Join(container, "worktrees", "existing")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse error = %v", err)
	}
	if strings.TrimSpace(string(out)) != "existing" {
		t.Fatalf("worktree branch = %q, want existing", strings.TrimSpace(string(out)))
	}
}

func TestEnsureWorktreeReturnsExistingPath(t *testing.T) {
	gitDir, container := seedBareRepo(t)
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	first, err := ensureWorktree(store, "feature")
	if err != nil {
		t.Fatalf("ensureWorktree() first error = %v", err)
	}
	second, err := ensureWorktree(store, "feature")
	if err != nil {
		t.Fatalf("ensureWorktree() second error = %v", err)
	}
	if first != second {
		t.Fatalf("second call path = %q, want %q", second, first)
	}
}

func TestEnsureWorktreeRejectsInvalidBranch(t *testing.T) {
	gitDir, container := seedBareRepo(t)
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	_, err := ensureWorktree(store, "feature.lock")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid branch name") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "invalid branch name")
	}
}

func TestEnsureWorktreeOrphanOnEmptyRepo(t *testing.T) {
	container := t.TempDir()
	gitDir := filepath.Join(container, ".bare")
	runGit(t, "", "init", "--bare", gitDir)

	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}
	path, err := ensureWorktree(store, "feature")
	if err != nil {
		t.Fatalf("ensureWorktree() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree missing: %v", err)
	}
	out, err := exec.Command("git", "-C", path, "symbolic-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("symbolic-ref error = %v", err)
	}
	if strings.TrimSpace(string(out)) != "refs/heads/feature" {
		t.Fatalf("orphan HEAD = %q, want refs/heads/feature", strings.TrimSpace(string(out)))
	}
}

func TestEnsureWorktreeRunCommandFailure(t *testing.T) {
	gitDir, container := seedBareRepo(t)
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	defer stubExecCommandExcept(t, "gh", "mise")()
	t.Setenv("GG_TEST_FAIL_ON_EXACT_ARG", "add")

	_, err := ensureWorktree(store, "feature")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create worktree") {
		t.Fatalf("error = %q, want substring 'create worktree'", err.Error())
	}
}

func TestEnsureWorktreeFinalizeFailure(t *testing.T) {
	gitDir, container := seedBareRepo(t)
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "git" && len(args) > 0 && args[0] == "submodule" {
			cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
			cmdArgs = append(cmdArgs, args...)
			cmd := exec.Command(os.Args[0], cmdArgs...)
			cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_COMMAND_EXIT=1")
			return cmd
		}
		return exec.Command(name, args...)
	}

	_, err := ensureWorktree(store, "feature")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "update submodules") {
		t.Fatalf("error = %q, want substring 'update submodules'", err.Error())
	}
}

func stubExecCommandExcept(t *testing.T, passthrough ...string) func() {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)
	allow := map[string]bool{}
	for _, name := range passthrough {
		allow[name] = true
	}
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if allow[name] {
			return exec.Command(name, args...)
		}
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}
	return func() {
		execCommand = oldExec
	}
}

func stubDefaultBranchGit(t *testing.T, envForArgs func([]string) []string) func() {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		if slices.Contains(args, "symbolic-ref") {
			env = append(env, "GG_TEST_COMMAND_STDOUT=origin/main")
		}
		env = append(env, envForArgs(args)...)
		cmd.Env = env
		return cmd
	}

	return func() {
		execCommand = oldExec
	}
}

func TestEnsurePRWorktreeRejectsUnmanaged(t *testing.T) {
	store := RepoStore{ContainerPath: "/tmp/local", MainPath: "/tmp/local", Managed: false}
	_, err := ensurePRWorktree(store, 1)
	if err == nil {
		t.Fatal("ensurePRWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "only supported for managed repositories") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestEnsurePRWorktreeRejectsEmptyRepo(t *testing.T) {
	container := t.TempDir()
	gitDir := filepath.Join(container, ".bare")
	runGit(t, "", "init", "--bare", gitDir)

	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}
	_, err := ensurePRWorktree(store, 7)
	if err == nil {
		t.Fatal("ensurePRWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "no commits yet") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "no commits yet")
	}
}

func TestEnsurePRWorktreeCheckoutFlow(t *testing.T) {
	gitDir, container := seedBareRepo(t)
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	defer stubExecCommandExcept(t, "git")()

	path, err := ensurePRWorktree(store, 42)
	if err != nil {
		t.Fatalf("ensurePRWorktree() error = %v", err)
	}
	want := filepath.Join(container, "PR", "42")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("PR worktree missing: %v", err)
	}

	commands := readCommandLog(t)
	wantCmds := []string{
		"gh\tpr\tcheckout\t42\t--detach",
	}
	if len(commands) != len(wantCmds) {
		t.Fatalf("commands = %v, want %v", commands, wantCmds)
	}
	for i := range wantCmds {
		if commands[i] != wantCmds[i] {
			t.Fatalf("commands[%d] = %q, want %q", i, commands[i], wantCmds[i])
		}
	}
}

func TestEnsurePRWorktreeReturnsExistingPath(t *testing.T) {
	gitDir, container := seedBareRepo(t)
	prPath := filepath.Join(container, "PR", "7")
	if err := os.MkdirAll(prPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}
	got, err := ensurePRWorktree(store, 7)
	if err != nil {
		t.Fatalf("ensurePRWorktree() error = %v", err)
	}
	if got != prPath {
		t.Fatalf("ensurePRWorktree() = %q, want %q", got, prPath)
	}
}

func TestEnsureRepoStoreFullCloneFlow(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_SIMULATE_MKDIR", "1")

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}
	if !store.Managed {
		t.Fatal("store.Managed = false, want true")
	}
	if store.GitDir != repo.BarePath(cfg) {
		t.Fatalf("store.GitDir = %q, want %q", store.GitDir, repo.BarePath(cfg))
	}
	if store.MainPath != repo.MainPath(cfg) {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, repo.MainPath(cfg))
	}

	gitDir := repo.BarePath(cfg)
	mainPath := repo.MainPath(cfg)
	want := []string{
		"git\tclone\t--bare\t--recursive\thttps://github.com/owner/repo.git\t" + gitDir,
		"git\t--git-dir\t" + gitDir + "\tfor-each-ref\t--count=1\t--format=%(refname)",
		"git\t--git-dir\t" + gitDir + "\tconfig\tremote.origin.fetch\t+refs/heads/*:refs/remotes/origin/*",
		"git\t--git-dir\t" + gitDir + "\tworktree\tadd\t--orphan\t-b\tmain\t" + mainPath,
		"git\tsubmodule\tupdate\t--init\t--recursive",
	}

	commands := readCommandLog(t)
	if len(commands) != len(want) {
		t.Fatalf("len(commands) = %d, want %d (%v)", len(commands), len(want), commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("commands[%d] = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestEnsureRepoStoreRepairsRemoteTrackingForExistingManagedRepo(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	container := repo.ContainerPath(cfg)
	gitDir := repo.BarePath(cfg)
	mainPath := repo.MainPath(cfg)
	for _, path := range []string{container, gitDir, mainPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	fetched := false
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")

		switch {
		case slices.Contains(args, "fetch"):
			fetched = true
		case slices.Contains(args, "for-each-ref"):
			env = append(env, "GG_TEST_COMMAND_STDOUT=refs/heads/main")
		case slices.Contains(args, "symbolic-ref"):
			if fetched {
				env = append(env, "GG_TEST_COMMAND_STDOUT=origin/main")
			} else {
				env = append(env, "GG_TEST_COMMAND_EXIT=1")
			}
		case slices.Contains(args, "rev-parse"):
			for _, arg := range args {
				if arg == "origin/main" || arg == "origin/master" {
					env = append(env, "GG_TEST_COMMAND_EXIT=1")
					break
				}
			}
		}

		cmd.Env = env
		return cmd
	}

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}
	if store.MainPath != mainPath {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, mainPath)
	}

	commands := readCommandLog(t)
	wantLines := []string{
		"git\t--git-dir\t" + gitDir + "\tconfig\tremote.origin.fetch\t+refs/heads/*:refs/remotes/origin/*",
		"git\t--git-dir\t" + gitDir + "\tfetch\torigin",
		"git\tbranch\t--set-upstream-to=origin/main\tmain",
	}
	for _, want := range wantLines {
		if !slices.Contains(commands, want) {
			t.Fatalf("commands missing %q\n%v", want, commands)
		}
	}
}

func TestEnsureRepoStoreCloneFailure(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "1")

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "clone owner/repo") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "clone owner/repo")
	}
}

func TestEnsureRepoStoreConfigFailure(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_SIMULATE_MKDIR", "1")
	t.Setenv("GG_TEST_FAIL_ON_EXACT_ARG", "config")

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "configure remote tracking") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestEnsureRepoStoreFetchFailure(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_SIMULATE_MKDIR=1")

		switch {
		case slices.Contains(args, "for-each-ref"):
			env = append(env, "GG_TEST_COMMAND_STDOUT=refs/heads/main")
		case slices.Contains(args, "symbolic-ref"):
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		case slices.Contains(args, "rev-parse"):
			for _, arg := range args {
				if arg == "origin/main" || arg == "origin/master" {
					env = append(env, "GG_TEST_COMMAND_EXIT=1")
					break
				}
			}
		case slices.Contains(args, "fetch"):
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		}

		cmd.Env = env
		return cmd
	}

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "fetch remote tracking refs") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestEnsureRepoStoreEmptyRepoWorktreeFailure(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_SIMULATE_MKDIR", "1")
	t.Setenv("GG_TEST_FAIL_ON_EXACT_ARG", "--orphan")

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty repository") {
		t.Fatalf("error = %q, want substring 'empty repository'", err.Error())
	}
}

func TestResolveRequestTwoArgsStandaloneError(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com", Aliases: map[string]string{}}

	_, err := resolveRequest(cfg, []string{"a/b/c", "feature"})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "owner/repo") {
		t.Fatalf("error = %q, want substring 'owner/repo'", err.Error())
	}
}

func TestResolveRequestTwoArgsFallsThroughToTwoArg(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com", Aliases: map[string]string{}}

	_, err := resolveRequest(cfg, []string{"", "repo"})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty repository alias") {
		t.Fatalf("error = %q, want substring 'empty repository alias'", err.Error())
	}
}

func TestResolveRequestThreeArgsResolveTwoArgsError(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com", Aliases: map[string]string{}}

	_, err := resolveRequest(cfg, []string{"", "repo", "feature"})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty repository alias") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestResolveRequestThreeArgsParseTargetError(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com", Aliases: map[string]string{}}

	_, err := resolveRequest(cfg, []string{"owner", "repo", "   "})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "worktree name cannot be empty") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestResolveRequestTwoArgsParseTargetEmpty(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com", Aliases: map[string]string{}}

	_, err := resolveRequest(cfg, []string{"owner/repo", "   "})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "worktree name cannot be empty") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestShellInitExecutablePathError(t *testing.T) {
	defer withStubExecutableFunc(t, func() (string, error) {
		return "", fmt.Errorf("boom")
	})()

	_, err := shellInit("fish")
	if err == nil {
		t.Fatal("shellInit() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "resolve executable path") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "resolve executable path")
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := []struct {
		name          string
		contents      string
		wantErrSubstr string
	}{
		{name: "invalid JSON", contents: `{not valid`, wantErrSubstr: "parse config"},
		{name: "unknown field", contents: `{"unknown":1}`, wantErrSubstr: "unknown field"},
		{name: "host with slash", contents: `{"host":"github.com/x"}`, wantErrSubstr: "must not contain"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(configPath, []byte(tc.contents), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			t.Setenv("GG_CONFIG", configPath)
			t.Setenv("HOME", "/tmp/gg-home")

			_, _, err := loadConfig()
			if err == nil {
				t.Fatalf("loadConfig() error = nil, want error")
			}
			if tc.wantErrSubstr != "" && !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Fatalf("loadConfig() error = %q, want substring %q", err.Error(), tc.wantErrSubstr)
			}
		})
	}
}

func TestLoadConfigRejectsEmptyHost(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(configPath, []byte(`{"host":"   "}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", "/tmp/gg-home")

	_, _, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "host cannot be empty") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "host cannot be empty")
	}
}

func TestDefaultConfigUserHomeDirError(t *testing.T) {
	t.Setenv("HOME", "")

	_, err := defaultConfig()
	if err == nil {
		t.Fatal("defaultConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "resolve home directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "resolve home directory")
	}
}

func TestConfigPathUsesUserConfigDir(t *testing.T) {
	t.Setenv("GG_CONFIG", "")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got, err := configPath()
	if err != nil {
		t.Fatalf("configPath() error = %v", err)
	}
	want := filepath.Join(xdg, "gg", "config")
	if got != want {
		t.Fatalf("configPath() = %q, want %q", got, want)
	}
}

func TestExpandPathVariants(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		name          string
		input         string
		want          string
		wantErrSubstr string
	}{
		{name: "tilde alone", input: "~", want: home},
		{name: "tilde slash", input: "~/work/src", want: filepath.Join(home, "work", "src")},
		{name: "env variable", input: "$HOME/projects", want: filepath.Join(home, "projects")},
		{name: "absolute path stays", input: "/opt/data", want: "/opt/data"},
		{name: "empty input", input: "", wantErrSubstr: "path is empty"},
		{name: "whitespace only", input: "   ", wantErrSubstr: "path is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandPath(tc.input)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expandPath(%q) error = nil, want substring %q", tc.input, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("expandPath(%q) error = %q, want substring %q", tc.input, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandPath(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("expandPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestInitConfigCommandErrors(t *testing.T) {
	t.Run("default config fails", func(t *testing.T) {
		t.Setenv("HOME", "")
		err := initConfigCommand()
		if err == nil {
			t.Fatal("initConfigCommand() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "resolve home directory") {
			t.Fatalf("error = %q, want substring", err.Error())
		}
	})

	t.Run("config path fails", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("GG_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "")
		err := initConfigCommand()
		if err == nil {
			t.Fatal("initConfigCommand() error = nil, want error")
		}
	})

}

func TestAliasCommandLoadErrors(t *testing.T) {
	t.Run("list mode load fails", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(configPath, []byte(`{not json`), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		t.Setenv("GG_CONFIG", configPath)
		t.Setenv("HOME", t.TempDir())

		err := aliasCommand(nil)
		if err == nil {
			t.Fatal("aliasCommand(nil) error = nil, want error")
		}
		if !strings.Contains(err.Error(), "parse config") {
			t.Fatalf("error = %q, want substring", err.Error())
		}
	})

	t.Run("set mode load fails", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(configPath, []byte(`{not json`), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		t.Setenv("GG_CONFIG", configPath)
		t.Setenv("HOME", t.TempDir())

		err := aliasCommand([]string{"ForkbombEu/credimi", "fc"})
		if err == nil {
			t.Fatal("aliasCommand() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "parse config") {
			t.Fatalf("error = %q, want substring", err.Error())
		}
	})

}

func TestAliasCommandWriteConfigError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())

	oldWriteFile := osWriteFile
	defer func() { osWriteFile = oldWriteFile }()
	osWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("simulated write failure")
	}

	err := aliasCommand([]string{"ForkbombEu/credimi", "fc"})
	if err == nil {
		t.Fatal("aliasCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "write config") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "write config")
	}
}

func TestLoadConfigEmptyFileReturnsDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(configPath, []byte("   \n\t"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	home := t.TempDir()
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", home)

	cfg, gotPath, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if gotPath != configPath {
		t.Fatalf("path = %q, want %q", gotPath, configPath)
	}
	if cfg.Host != "github.com" {
		t.Fatalf("Host = %q, want github.com", cfg.Host)
	}
	wantRoot := filepath.Join(home, "src")
	if cfg.Root != wantRoot {
		t.Fatalf("Root = %q, want %q", cfg.Root, wantRoot)
	}
}

func TestLoadConfigDefaultConfigFails(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("GG_CONFIG", filepath.Join(t.TempDir(), "config"))

	_, _, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "resolve home directory") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestLoadConfigExpandRootFails(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(configPath, []byte(`{"root":"   "}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())

	_, _, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "expand root") {
		t.Fatalf("error = %q, want substring 'expand root'", err.Error())
	}
}

func TestPathCommandEnsureRequestError(t *testing.T) {
	cfg := setupTestConfig(t)
	localPath := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localPath, ".git"), []byte("not a gitdir"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := pathCommand([]string{"owner/repo", "PR/999"})
	if err == nil {
		t.Fatal("pathCommand() error = nil, want error")
	}
}

func TestManageCommandsPropagateLoadConfigError(t *testing.T) {
	cases := []struct {
		name string
		fn   func([]string) error
	}{
		{name: "list", fn: listCommand},
		{name: "status", fn: statusCommand},
		{name: "prune", fn: pruneCommand},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(configPath, []byte(`{not json`), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			t.Setenv("GG_CONFIG", configPath)
			t.Setenv("HOME", t.TempDir())

			err := tc.fn([]string{"owner/repo"})
			if err == nil {
				t.Fatalf("%s() error = nil, want error", tc.name)
			}
			if !strings.Contains(err.Error(), "parse config") {
				t.Fatalf("%s error = %q, want substring 'parse config'", tc.name, err.Error())
			}
		})
	}
}

func TestManageCommandsRejectMissingRepo(t *testing.T) {
	cases := []struct {
		name string
		fn   func([]string) error
		args []string
	}{
		{name: "list too many args", fn: listCommand, args: []string{"a", "b", "c"}},
		{name: "status too many args", fn: statusCommand, args: []string{"a", "b", "c"}},
		{name: "prune too many args", fn: pruneCommand, args: []string{"a", "b", "c"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupTestConfig(t)
			err := tc.fn(tc.args)
			if err == nil {
				t.Fatalf("%s() error = nil, want error", tc.name)
			}
			if !strings.Contains(err.Error(), "usage:") {
				t.Fatalf("%s error = %q, want substring 'usage:'", tc.name, err.Error())
			}
		})
	}
}

func TestManageCommandsFindRepoStoreError(t *testing.T) {
	cases := []struct {
		name string
		fn   func([]string) error
	}{
		{name: "list", fn: listCommand},
		{name: "status", fn: statusCommand},
		{name: "prune", fn: pruneCommand},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupTestConfig(t)
			err := tc.fn([]string{"owner/notthere"})
			if err == nil {
				t.Fatalf("%s() error = nil, want error", tc.name)
			}
			if !strings.Contains(err.Error(), "not available locally") {
				t.Fatalf("%s error = %q, want substring", tc.name, err.Error())
			}
		})
	}
}

func TestStatusCommandReadRepoStatusError(t *testing.T) {
	cfg := setupTestConfig(t)
	containerPath := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	if err := os.MkdirAll(filepath.Join(containerPath, ".bare"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	mainPath := filepath.Join(containerPath, "main")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	err := statusCommand([]string{"owner/repo"})
	if err == nil {
		t.Fatal("statusCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "status for") {
		t.Fatalf("error = %q, want substring 'status for'", err.Error())
	}
}

func TestRunDispatchesAlias(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())

	output, err := captureStdout(t, func() error {
		return run([]string{"alias", "ForkbombEu/credimi", "fc"})
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(output, "fc -> ForkbombEu/credimi") {
		t.Fatalf("output = %q, want substring 'fc -> ForkbombEu/credimi'", output)
	}
}

func TestRunDispatchesInitConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())

	output, err := captureStdout(t, func() error {
		return run([]string{"init-config"})
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if strings.TrimSpace(output) != configPath {
		t.Fatalf("output = %q, want %q", strings.TrimSpace(output), configPath)
	}
}

func TestRunDispatchesListAndLs(t *testing.T) {
	cfg := setupTestConfig(t)
	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	seedBareRepoAt(t, container)

	for _, name := range []string{"list", "ls"} {
		t.Run(name, func(t *testing.T) {
			output, err := captureStdout(t, func() error {
				return run([]string{name, "owner/repo"})
			})
			if err != nil {
				t.Fatalf("run(%s) error = %v", name, err)
			}
			if !strings.Contains(output, "main") {
				t.Fatalf("run(%s) output = %q, want 'main'", name, output)
			}
		})
	}
}

func TestRunDispatchesStatus(t *testing.T) {
	cfg := setupTestConfig(t)
	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	seedBareRepoAt(t, container)

	output, err := captureStdout(t, func() error {
		return run([]string{"status", "owner/repo"})
	})
	if err != nil {
		t.Fatalf("run(status) error = %v", err)
	}
	if !strings.Contains(output, "status  clean") {
		t.Fatalf("output = %q, want 'status  clean'", output)
	}
}

func TestRunDispatchesPruneAndRm(t *testing.T) {
	for _, name := range []string{"prune", "rm"} {
		t.Run(name, func(t *testing.T) {
			cfg := setupTestConfig(t)
			container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
			seedBareRepoAt(t, container)

			output, err := captureStdout(t, func() error {
				return run([]string{name, "owner/repo"})
			})
			if err != nil {
				t.Fatalf("run(%s) error = %v", name, err)
			}
			if output == "" {
				t.Fatalf("run(%s) output = empty, want something", name)
			}
		})
	}
}

func TestRunConfigPathError(t *testing.T) {
	t.Setenv("GG_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	_, err := captureStdout(t, func() error {
		return run([]string{"config-path"})
	})
	if err == nil {
		t.Fatal("run(config-path) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "resolve config directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "resolve config directory")
	}
}

func TestPathCommandLoadConfigError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(configPath, []byte(`{not json`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", "/tmp/gg-home")

	err := pathCommand([]string{"owner/repo"})
	if err == nil {
		t.Fatal("pathCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "parse config")
	}
}

func TestPathCommandResolveError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", t.TempDir())

	err := pathCommand([]string{"a", "b", "c", "d"})
	if err == nil {
		t.Fatal("pathCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "usage:")
	}
}

func TestResolveRequestRejectsFourArgs(t *testing.T) {
	_, err := resolveRequest(Config{Host: "github.com"}, []string{"a", "b", "c", "d"})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "usage:")
	}
}

func TestParseTargetRejectsEmpty(t *testing.T) {
	_, err := parseTarget("   ")
	if err == nil {
		t.Fatal("parseTarget() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "worktree name cannot be empty") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestResolveOwnerRejectsEmptyAliasExpansion(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"empty": "   ",
		},
	}
	_, err := resolveOwner(cfg, "empty")
	if err == nil {
		t.Fatal("resolveOwner() error = nil, want error")
	}
}

func TestResolveCombinedAliasBadSpec(t *testing.T) {
	cfg := Config{
		Host: "github.com",
		Aliases: map[string]string{
			"foo/bar": "no-slash",
		},
	}
	_, err := resolveCombinedAlias(cfg, "foo", "bar")
	if err == nil {
		t.Fatal("resolveCombinedAlias() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "must be in the form owner/repo") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestTryResolveStandaloneRepoSlashMalformed(t *testing.T) {
	cfg := Config{Host: "github.com"}
	_, ok, err := tryResolveStandaloneRepo(cfg, "a/b/c")
	if err == nil {
		t.Fatal("tryResolveStandaloneRepo() error = nil, want error")
	}
	if ok {
		t.Fatal("tryResolveStandaloneRepo() ok = true, want false")
	}
}

func TestTryResolveStandaloneRepoNoSlashMiss(t *testing.T) {
	cfg := Config{Host: "github.com"}
	_, ok, err := tryResolveStandaloneRepo(cfg, "owner")
	if err != nil {
		t.Fatalf("tryResolveStandaloneRepo() error = %v", err)
	}
	if ok {
		t.Fatal("tryResolveStandaloneRepo() ok = true, want false")
	}
}

func TestDirectoryExistsRejectsFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(filePath, []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := directoryExists(filePath)
	if err == nil {
		t.Fatal("directoryExists() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "not a directory")
	}
}

func TestEnsureRequestOwnerBranch(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Host: "github.com"}

	path, err := ensureRequest(cfg, Request{
		Owner:  "newowner",
		Target: Target{Kind: TargetOwner},
	})
	if err != nil {
		t.Fatalf("ensureRequest() error = %v", err)
	}
	want := filepath.Join(root, "github.com", "newowner")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("owner dir not created: %v", err)
	}
}

func TestEnsureRequestWorktreeBranch(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}
	container := repo.ContainerPath(cfg)
	seedBareRepoAt(t, container)

	request := Request{
		Repo:   repo,
		Target: Target{Kind: TargetWorktree, WorktreeName: "feature"},
	}
	path, err := ensureRequest(cfg, request)
	if err != nil {
		t.Fatalf("ensureRequest() error = %v", err)
	}
	want := filepath.Join(container, "worktrees", "feature")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestEnsureRequestRepoBranch(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}
	container := repo.ContainerPath(cfg)
	_, mainPath := seedBareRepoAt(t, container)

	request := Request{Repo: repo, Target: Target{Kind: TargetRepo}}
	path, err := ensureRequest(cfg, request)
	if err != nil {
		t.Fatalf("ensureRequest() error = %v", err)
	}
	if path != mainPath {
		t.Fatalf("path = %q, want %q", path, mainPath)
	}
}

func TestEnsureRequestUnsupportedKind(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}
	seedBareRepoAt(t, repo.ContainerPath(cfg))

	_, err := ensureRequest(cfg, Request{Repo: repo, Target: Target{Kind: TargetKind(99)}})
	if err == nil {
		t.Fatal("ensureRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unsupported target kind") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestEnsureRepoStoreReturnsLocalForLocalDirectory(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "local"}
	localPath := repo.ContainerPath(cfg)
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localPath, "file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}
	if store.Managed {
		t.Fatal("store.Managed = true, want false for local directory")
	}
	if store.MainPath != localPath {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, localPath)
	}
}

func TestEnsureRepoStoreRecreatesMainFromExistingBare(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}
	gitDir, mainPath := seedBareRepoAt(t, repo.ContainerPath(cfg))

	if err := os.RemoveAll(mainPath); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	runGit(t, "", "--git-dir", gitDir, "worktree", "prune")

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}
	if !store.Managed {
		t.Fatal("store.Managed = false, want true")
	}
	if store.MainPath != mainPath {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, mainPath)
	}
	if _, err := os.Stat(mainPath); err != nil {
		t.Fatalf("main path not recreated: %v", err)
	}
}

func TestEnsureRepoStoreReusesExistingMain(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}
	_, mainPath := seedBareRepoAt(t, repo.ContainerPath(cfg))

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}
	if !store.Managed {
		t.Fatal("store.Managed = false, want true")
	}
	if store.MainPath != mainPath {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, mainPath)
	}
}

func TestRepoHasRefsErrorOnMissingDir(t *testing.T) {
	_, err := repoHasRefs(filepath.Join(t.TempDir(), "missing.git"))
	if err == nil {
		t.Fatal("repoHasRefs() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "check refs") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "check refs")
	}
}

func TestUpdateSubmodulesFailure(t *testing.T) {
	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "1")

	err := updateSubmodules(t.TempDir())
	if err == nil {
		t.Fatal("updateSubmodules() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "update submodules") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "update submodules")
	}
}

func TestFinalizeWorktreeSetupSubmoduleFailure(t *testing.T) {
	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "1")

	err := finalizeWorktreeSetup(t.TempDir(), Repo{})
	if err == nil {
		t.Fatal("finalizeWorktreeSetup() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "update submodules") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "update submodules")
	}
}

func TestSetupMiseToolingTrustFailure(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "mise.toml"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "1")

	err := setupMiseTooling(worktree)
	if err == nil {
		t.Fatal("setupMiseTooling() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "trust mise config") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "trust mise config")
	}
}

func TestSetupMiseToolingFindConfigError(t *testing.T) {
	worktree := t.TempDir()

	oldStat := osStat
	defer func() { osStat = oldStat }()
	osStat = func(path string) (os.FileInfo, error) {
		if path == filepath.Join(worktree, "mise.toml") {
			return nil, errors.New("simulated stat failure")
		}
		return os.Stat(path)
	}

	err := setupMiseTooling(worktree)
	if err == nil {
		t.Fatal("setupMiseTooling() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Fatalf("error = %q, want stat error", err.Error())
	}
}

func TestSetupMiseToolingHandlesBothConfigs(t *testing.T) {
	worktree := t.TempDir()
	for _, name := range []string{"mise.toml", ".mise.toml"} {
		if err := os.WriteFile(filepath.Join(worktree, name), []byte{}, 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	defer stubExecCommand(t)()

	if err := setupMiseTooling(worktree); err != nil {
		t.Fatalf("setupMiseTooling() error = %v", err)
	}

	commands := readCommandLog(t)
	want := []string{
		"mise\ttrust\t" + filepath.Join(worktree, "mise.toml"),
		"mise\ttrust\t" + filepath.Join(worktree, ".mise.toml"),
		"mise\tinstall",
	}
	if len(commands) != len(want) {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("commands[%d] = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestClassifyExistingRepoPathEmpty(t *testing.T) {
	container := t.TempDir()
	store := RepoStore{
		ContainerPath: container,
		GitDir:        filepath.Join(container, ".bare"),
		MainPath:      filepath.Join(container, "main"),
	}
	got, err := classifyExistingRepoPath(store)
	if err != nil {
		t.Fatalf("classifyExistingRepoPath() error = %v", err)
	}
	if got != "empty" {
		t.Fatalf("classifyExistingRepoPath() = %q, want %q", got, "empty")
	}
}

func TestClassifyExistingRepoPathLocal(t *testing.T) {
	container := t.TempDir()
	if err := os.WriteFile(filepath.Join(container, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := RepoStore{
		ContainerPath: container,
		GitDir:        filepath.Join(container, ".bare"),
		MainPath:      filepath.Join(container, "main"),
	}
	got, err := classifyExistingRepoPath(store)
	if err != nil {
		t.Fatalf("classifyExistingRepoPath() error = %v", err)
	}
	if got != "local" {
		t.Fatalf("classifyExistingRepoPath() = %q, want %q", got, "local")
	}
}

func TestClassifyExistingRepoPathManaged(t *testing.T) {
	container := t.TempDir()
	store := RepoStore{
		ContainerPath: container,
		GitDir:        filepath.Join(container, ".bare"),
		MainPath:      filepath.Join(container, "main"),
	}
	if err := os.MkdirAll(store.GitDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	got, err := classifyExistingRepoPath(store)
	if err != nil {
		t.Fatalf("classifyExistingRepoPath() error = %v", err)
	}
	if got != "managed" {
		t.Fatalf("classifyExistingRepoPath() = %q, want %q", got, "managed")
	}
}

func TestDiscoverEntriesSkipDirAfterMatch(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "nested")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(outer, ".git"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(inner, ".git"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	entries, err := discoverEntries(root, "worktree")
	if err != nil {
		t.Fatalf("discoverEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (SkipDir must prevent nested recursion: %v)", len(entries), entries)
	}
	if entries[0].Name != "outer" {
		t.Fatalf("entry.Name = %q, want outer", entries[0].Name)
	}
}

func TestDiscoverEntriesMissingRoot(t *testing.T) {
	entries, err := discoverEntries(filepath.Join(t.TempDir(), "missing"), "worktree")
	if err != nil {
		t.Fatalf("discoverEntries() error = %v", err)
	}
	if entries != nil {
		t.Fatalf("entries = %v, want nil", entries)
	}
}

func TestListRepoEntriesManagedSkipsMissingMain(t *testing.T) {
	container := t.TempDir()
	store := RepoStore{
		ContainerPath: container,
		MainPath:      filepath.Join(container, "main"),
		Managed:       true,
	}
	if err := os.MkdirAll(filepath.Join(container, "worktrees", "wt1"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(container, "worktrees", "wt1", ".git"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		t.Fatalf("listRepoEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Kind != "worktree" {
		t.Fatalf("entries[0].Kind = %q, want worktree", entries[0].Kind)
	}
}

func TestShellInitResolvesSymlinks(t *testing.T) {
	realBin := filepath.Join(t.TempDir(), "real-gg")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	linkBin := filepath.Join(t.TempDir(), "gg-link")
	if err := os.Symlink(realBin, linkBin); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	defer withStubExecutable(t, linkBin)()

	script, err := shellInit("fish")
	if err != nil {
		t.Fatalf("shellInit() error = %v", err)
	}
	resolved, err := filepath.EvalSymlinks(realBin)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if !strings.Contains(script, resolved) {
		t.Fatalf("script missing resolved path %q:\n%s", resolved, script)
	}
	if strings.Contains(script, linkBin) {
		t.Fatalf("script should not embed symlink %q:\n%s", linkBin, script)
	}
}

func TestPruneCommandGitFailure(t *testing.T) {
	cfg := setupTestConfig(t)
	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "1")

	err := pruneCommand([]string{"owner/repo"})
	if err == nil {
		t.Fatal("pruneCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "prune worktrees") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "prune worktrees")
	}
}

func TestStatusCommandMissingRepoArgs(t *testing.T) {
	err := statusCommand(nil)
	if err == nil {
		t.Fatal("statusCommand(nil) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "usage: gg status") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestListCommandMissingRepo(t *testing.T) {
	setupTestConfig(t)

	err := listCommand([]string{"missing/repo"})
	if err == nil {
		t.Fatal("listCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "not available locally") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func TestFindRepoStoreRejectsContainerPathIsFile(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Host: "github.com"}
	ownerDir := filepath.Join(root, "github.com", "owner")
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(ownerDir, "repo"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := findRepoStore(cfg, Repo{Owner: "owner", Name: "repo"})
	if err == nil {
		t.Fatal("findRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %q, want substring", err.Error())
	}
}

func stubExecCommand(t *testing.T) func() {
	t.Helper()

	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExecCommand := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}

	return func() {
		execCommand = oldExecCommand
	}
}

func stubNewCommandGit(t *testing.T, failArg string, withMarkdown bool) func() {
	t.Helper()

	oldExecCommand := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "git" && len(args) == 5 && args[0] == "clone" && args[4] != "" {
			templateRoot := args[4]
			if err := os.MkdirAll(templateRoot, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			fileName := "README.txt"
			if withMarkdown {
				fileName = "README.md"
			}
			if err := os.WriteFile(filepath.Join(templateRoot, fileName), []byte("template\n"), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
		}

		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		if failArg != "" {
			env = append(env, "GG_TEST_FAIL_ON_EXACT_ARG="+failArg)
		}
		cmd.Env = env
		return cmd
	}

	return func() {
		execCommand = oldExecCommand
	}
}

func stubExecLookPath(t *testing.T, fn func(string) (string, error)) func() {
	t.Helper()

	oldExecLookPath := execLookPath
	execLookPath = fn
	return func() {
		execLookPath = oldExecLookPath
	}
}

func readCommandLog(t *testing.T) []string {
	t.Helper()

	logPath := os.Getenv("GG_TEST_COMMAND_LOG")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", logPath, err)
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil
	}

	return strings.Split(content, "\n")
}

func TestHelperProcess(t *testing.T) { //nolint:unparam
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator == -1 || separator+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "missing helper separator")
		os.Exit(2)
	}

	name := args[separator+1]
	cmdArgs := args[separator+2:]

	logPath := os.Getenv("GG_TEST_COMMAND_LOG")
	if logPath != "" {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer file.Close() //nolint:errcheck

		line := name
		if len(cmdArgs) > 0 {
			line += "\t" + strings.Join(cmdArgs, "\t")
		}
		if _, err := fmt.Fprintln(file, line); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	// GG_TEST_SIMULATE_MKDIR=1 tells the stubbed helper to mkdir the destination
	// arg of `git clone` or `git worktree add` so downstream directoryExists
	// checks pass when the git command itself is stubbed.
	if os.Getenv("GG_TEST_SIMULATE_MKDIR") == "1" && name == "git" {
		relevant := false
		for i, a := range cmdArgs {
			if a == "clone" || (a == "worktree" && i+1 < len(cmdArgs) && cmdArgs[i+1] == "add") {
				relevant = true
				break
			}
		}
		if relevant {
			for j := len(cmdArgs) - 1; j >= 0; j-- {
				if filepath.IsAbs(cmdArgs[j]) {
					_ = os.MkdirAll(cmdArgs[j], 0o755)
					break
				}
			}
		}
	}

	if stdout := os.Getenv("GG_TEST_COMMAND_STDOUT"); stdout != "" {
		fmt.Print(stdout)
	}
	// GG_TEST_FAIL_ON_EXACT_ARG=<value> exits non-zero whenever any arg equals
	// <value> exactly. Lets tests target a specific git subcommand (e.g.
	// "fetch") without matching unrelated args like "remote.origin.fetch".
	if pattern := os.Getenv("GG_TEST_FAIL_ON_EXACT_ARG"); pattern != "" {
		for _, a := range cmdArgs {
			if a == pattern {
				os.Exit(1)
			}
		}
	}
	if exitCode := os.Getenv("GG_TEST_COMMAND_EXIT"); exitCode != "" {
		code, err := strconv.Atoi(exitCode)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(code)
	}

	os.Exit(0)
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stdout = w

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	callErr := fn()

	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	os.Stdout = old
	<-done

	return buf.String(), callErr
}

func withStubExecutable(t *testing.T, path string) func() {
	t.Helper()
	return withStubExecutableFunc(t, func() (string, error) {
		return path, nil
	})
}

func withStubExecutableFunc(t *testing.T, fn func() (string, error)) func() {
	t.Helper()
	old := executablePath
	executablePath = fn
	return func() {
		executablePath = old
	}
}

func TestSetupMiseToolingInstallFailure(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "mise.toml"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_FAIL_ON_EXACT_ARG", "install")

	err := setupMiseTooling(worktree)
	if err == nil {
		t.Fatal("setupMiseTooling() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "install mise tools") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "install mise tools")
	}
}

func TestEnsurePRWorktreeWorktreeAddFailure(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	// Fully stub every exec call so we can drive repoHasRefs / defaultBranchRef
	// via arg-aware stdout, then fail `git worktree add` on the "add" arg.
	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		for _, a := range args {
			if a == "for-each-ref" {
				env = append(env, "GG_TEST_COMMAND_STDOUT=refs/heads/main")
				break
			}
			if a == "symbolic-ref" {
				env = append(env, "GG_TEST_COMMAND_STDOUT=origin/main")
				break
			}
			if a == "add" {
				env = append(env, "GG_TEST_COMMAND_EXIT=1")
				break
			}
		}
		cmd.Env = env
		return cmd
	}

	_, err := ensurePRWorktree(store, 42)
	if err == nil {
		t.Fatal("ensurePRWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create PR worktree 42") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "create PR worktree 42")
	}
}

func TestEnsurePRWorktreeCheckoutFailure(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	defer stubExecCommandExcept(t, "git")()
	t.Setenv("GG_TEST_FAIL_ON_EXACT_ARG", "checkout")

	_, err := ensurePRWorktree(store, 42)
	if err == nil {
		t.Fatal("ensurePRWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "checkout PR 42") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "checkout PR 42")
	}
}

func TestEnsurePRWorktreeFinalizeFailure(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		// Let real git run for setup/checkout steps; stub only
		// `git submodule ...` (to fail) and every non-git command (to succeed).
		if name == "git" && (len(args) == 0 || args[0] != "submodule") {
			return exec.Command(name, args...)
		}
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		if name == "git" && len(args) > 0 && args[0] == "submodule" {
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		}
		cmd.Env = env
		return cmd
	}

	_, err := ensurePRWorktree(store, 42)
	if err == nil {
		t.Fatal("ensurePRWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "update submodules") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "update submodules")
	}
}

func TestEnsureWorktreeRepoHasRefsFailure(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		// Fail only `git for-each-ref ...` so repoHasRefs errors while
		// validateBranchName / localBranchExists keep running against the real
		// seeded repo.
		if name == "git" && slices.Contains(args, "for-each-ref") {
			cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
			cmdArgs = append(cmdArgs, args...)
			cmd := exec.Command(os.Args[0], cmdArgs...)
			cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_COMMAND_EXIT=1")
			return cmd
		}
		return exec.Command(name, args...)
	}

	_, err := ensureWorktree(store, "new-feature")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "check refs for") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "check refs for")
	}
}

func TestEnsureRepoStoreDefaultBranchRefFailure(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_SIMULATE_MKDIR=1")
		for _, a := range args {
			if a == "for-each-ref" {
				// repoHasRefs -> true
				env = append(env, "GG_TEST_COMMAND_STDOUT=refs/heads/main")
				break
			}
			if a == "symbolic-ref" {
				// Leave stdout empty so defaultBranchRef falls through to
				// candidate rev-parse probes.
				break
			}
			if a == "rev-parse" {
				// Every candidate fails so defaultBranchRef returns the
				// "could not determine default branch" error.
				env = append(env, "GG_TEST_COMMAND_EXIT=1")
				break
			}
		}
		cmd.Env = env
		return cmd
	}

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "could not determine default branch") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "could not determine default branch")
	}
}

func TestEnsureRepoStoreWorktreeAddFailure(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_SIMULATE_MKDIR", "1")
	// for-each-ref returns origin/main which is also accepted by symbolic-ref,
	// so defaultBranchRef succeeds and we reach `git worktree add main-branch`.
	t.Setenv("GG_TEST_COMMAND_STDOUT", "origin/main")
	t.Setenv("GG_TEST_FAIL_ON_EXACT_ARG", "add")

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create main worktree for owner/repo") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "create main worktree for owner/repo")
	}
}

func TestEnsureRepoStoreFinalizeFailure(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		envExtras := []string{"GO_WANT_HELPER_PROCESS=1", "GG_TEST_SIMULATE_MKDIR=1"}
		// for-each-ref must report refs so repoHasRefs is true; symbolic-ref
		// must return a ref so defaultBranchRef resolves.
		for _, a := range args {
			if a == "for-each-ref" || a == "symbolic-ref" {
				envExtras = append(envExtras, "GG_TEST_COMMAND_STDOUT=origin/main")
				break
			}
		}
		// Only `git submodule ...` should fail — this is the finalize step.
		if name == "git" && len(args) > 0 && args[0] == "submodule" {
			envExtras = append(envExtras, "GG_TEST_COMMAND_EXIT=1")
		}

		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(), envExtras...)
		return cmd
	}

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "update submodules") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "update submodules")
	}
}

func TestLoadConfigConfigPathFails(t *testing.T) {
	t.Setenv("GG_CONFIG", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	_, _, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "resolve config directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "resolve config directory")
	}
}

func TestExpandPathTildeHomeFails(t *testing.T) {
	t.Setenv("HOME", "")

	_, err := expandPath("~/projects")
	if err == nil {
		t.Fatal("expandPath() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "resolve home directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "resolve home directory")
	}
}

func TestEnsureRequestRejectsUnknownTargetKind(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}
	seedBareRepoAt(t, repo.ContainerPath(cfg))

	_, err := ensureRequest(cfg, Request{
		Repo:   repo,
		Target: Target{Kind: TargetKind(99)},
	})
	if err == nil {
		t.Fatal("ensureRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unsupported target kind") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "unsupported target kind")
	}
}

func TestEnsureRequestPropagatesEnsureRepoStoreError(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "1")

	_, err := ensureRequest(cfg, Request{
		Repo:   repo,
		Target: Target{Kind: TargetRepo},
	})
	if err == nil {
		t.Fatal("ensureRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "clone owner/repo") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "clone owner/repo")
	}
}

func TestResolveRequestOneArgBothResolversFail(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com", Aliases: map[string]string{}}

	_, err := resolveRequest(cfg, []string{"a/b/c"})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	// resolveOneArg's splitRepoSpec surfaces first.
	if !strings.Contains(err.Error(), "owner/repo") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "owner/repo")
	}
}

func TestResolveTwoArgsSecondArgExpandAliasFails(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com", Aliases: map[string]string{}}

	// First arg is valid, second arg is whitespace -> expandAlias errors
	// on "empty repository alias" from the second argument.
	_, err := resolveRequest(cfg, []string{"owner", "   ", "feature"})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty repository alias") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "empty repository alias")
	}
}

func TestResolveCombinedAliasErrorsOnCombinedCycle(t *testing.T) {
	// Alias that expands owner/repo -> cycle -> expandAlias errors inside
	// resolveCombinedAlias at the point line 271 covers.
	cfg := Config{
		Root: t.TempDir(),
		Host: "github.com",
		Aliases: map[string]string{
			"owner/repo": "owner/repo",
		},
	}

	_, err := resolveRequest(cfg, []string{"owner", "repo"})
	if err == nil {
		t.Fatal("resolveRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "alias cycle detected") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "alias cycle detected")
	}
}

func TestEnsureOwnerPathReusesExistingDirectory(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Host: "github.com"}
	existing := filepath.Join(root, "github.com", "owner")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	got, err := ensureOwnerPath(cfg, "owner")
	if err != nil {
		t.Fatalf("ensureOwnerPath() error = %v", err)
	}
	if got != existing {
		t.Fatalf("ensureOwnerPath() = %q, want %q", got, existing)
	}
}

func TestEnsureWorktreeRejectsEmptyName(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	_, err := ensureWorktree(store, "")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "path name cannot be empty") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "path name cannot be empty")
	}
}

func TestEnsureWorktreeLocalBranchExistsCheckFails(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		// rev-parse returning exit code 2 is NOT interpreted by
		// localBranchExists as "branch missing" — it surfaces as a wrapped
		// error. Let check-ref-format pass (real git) and all other calls
		// pass through.
		if name == "git" && slices.Contains(args, "rev-parse") {
			cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
			cmdArgs = append(cmdArgs, args...)
			cmd := exec.Command(os.Args[0], cmdArgs...)
			cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_COMMAND_EXIT=2")
			return cmd
		}
		return exec.Command(name, args...)
	}

	_, err := ensureWorktree(store, "feature")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "check branch") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "check branch")
	}
}

func TestEnsureWorktreeDefaultBaseRefFailure(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		switch {
		case slices.Contains(args, "check-ref-format"):
			// Pass through — real git validates branch name.
			return exec.Command(name, args...)
		case slices.Contains(args, "for-each-ref"):
			env = append(env, "GG_TEST_COMMAND_STDOUT=refs/heads/main")
		case slices.Contains(args, "rev-parse"):
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
			// symbolic-ref: falls through to empty stdout, exit 0.
		}
		cmd.Env = env
		return cmd
	}

	_, err := ensureWorktree(store, "feature")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "could not determine default branch") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "could not determine default branch")
	}
}

func TestEnsurePRWorktreeRepoHasRefsFailure(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "for-each-ref") {
			cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
			cmdArgs = append(cmdArgs, args...)
			cmd := exec.Command(os.Args[0], cmdArgs...)
			cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_COMMAND_EXIT=1")
			return cmd
		}
		return exec.Command(name, args...)
	}

	_, err := ensurePRWorktree(store, 42)
	if err == nil {
		t.Fatal("ensurePRWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "check refs for") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "check refs for")
	}
}

func TestEnsurePRWorktreeDefaultBaseRefFailure(t *testing.T) {
	_, container := seedBareRepo(t)
	gitDir := filepath.Join(container, ".bare")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		switch {
		case slices.Contains(args, "for-each-ref"):
			env = append(env, "GG_TEST_COMMAND_STDOUT=refs/heads/main")
		case slices.Contains(args, "rev-parse"):
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		}
		cmd.Env = env
		return cmd
	}

	_, err := ensurePRWorktree(store, 42)
	if err == nil {
		t.Fatal("ensurePRWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "could not determine default branch") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "could not determine default branch")
	}
}

func TestEnsureRepoStoreNewBranchPath(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_SIMULATE_MKDIR=1")
		hasLocalHeadsProbe := false
		for _, a := range args {
			if strings.HasPrefix(a, "refs/heads/") {
				hasLocalHeadsProbe = true
				break
			}
		}
		switch {
		case slices.Contains(args, "for-each-ref"):
			env = append(env, "GG_TEST_COMMAND_STDOUT=refs/heads/main")
		case slices.Contains(args, "symbolic-ref"):
			env = append(env, "GG_TEST_COMMAND_STDOUT=origin/main")
		case hasLocalHeadsProbe:
			// localBranchExists probe — branch doesn't exist locally.
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		}
		cmd.Env = env
		return cmd
	}

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}
	if store.MainPath != repo.MainPath(cfg) {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, repo.MainPath(cfg))
	}

	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	// Must contain the `-b main` branch-creation form, not the plain
	// `worktree add mainPath main` form.
	if !strings.Contains(string(commands), "worktree\tadd\t-b\tmain") {
		t.Fatalf("commands missing `worktree add -b main ...`\n%s", commands)
	}
}

func TestListRepoEntriesSortsSameKindByName(t *testing.T) {
	root := t.TempDir()
	store := RepoStore{
		ContainerPath: root,
		MainPath:      filepath.Join(root, "main"),
		Managed:       true,
	}

	// Two worktrees force sort.Slice to compare entries with equal Kind,
	// exercising the name-comparison branch.
	for _, name := range []string{"zzz-late", "aaa-early"} {
		path := filepath.Join(root, "worktrees", name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		t.Fatalf("listRepoEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Name != "aaa-early" || entries[1].Name != "zzz-late" {
		t.Fatalf("entries = [%q, %q], want [aaa-early, zzz-late]", entries[0].Name, entries[1].Name)
	}
}

func TestListCommandRejectsThreeArgs(t *testing.T) {
	setupTestConfig(t)

	err := listCommand([]string{"a", "b", "c"})
	if err == nil {
		t.Fatal("listCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "usage: gg <command>") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "usage: gg <command>")
	}
}

func TestStatusCommandRejectsThreeArgs(t *testing.T) {
	setupTestConfig(t)

	err := statusCommand([]string{"a", "b", "c"})
	if err == nil {
		t.Fatal("statusCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "usage: gg <command>") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "usage: gg <command>")
	}
}

func TestPruneCommandRejectsThreeArgs(t *testing.T) {
	setupTestConfig(t)

	err := pruneCommand([]string{"a", "b", "c"})
	if err == nil {
		t.Fatal("pruneCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "usage: gg <command>") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "usage: gg <command>")
	}
}

func TestStatusCommandMultipleEntriesPrintsSeparator(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	mainPath := filepath.Join(container, "main")
	worktreePath := filepath.Join(container, "worktrees", "feature")
	for _, dir := range []string{filepath.Join(container, ".bare"), mainPath, worktreePath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	for _, sentinel := range []string{
		filepath.Join(mainPath, ".git"),
		filepath.Join(worktreePath, ".git"),
	} {
		if err := os.WriteFile(sentinel, []byte("gitdir: x"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	defer stubExecCommand(t)()

	output, err := captureStdout(t, func() error {
		return statusCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("statusCommand() error = %v", err)
	}

	if strings.Count(output, "status  clean") != 2 {
		t.Fatalf("want two 'status  clean' entries in output:\n%s", output)
	}
	// Separator between entries is a blank line — there should be at least one
	// blank line in the output body.
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if !slices.Contains(lines, "") {
		t.Fatalf("expected blank separator line between entries in:\n%s", output)
	}
}

func TestPruneCommandPrintsGitOutput(t *testing.T) {
	cfg := setupTestConfig(t)

	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_STDOUT", "Removing worktrees/feature: gitdir file points to non-existent location\n")

	output, err := captureStdout(t, func() error {
		return pruneCommand([]string{"owner/repo"})
	})
	if err != nil {
		t.Fatalf("pruneCommand() error = %v", err)
	}
	if !strings.Contains(output, "Removing worktrees/feature") {
		t.Fatalf("output missing git prune stdout in:\n%s", output)
	}
	if strings.Contains(output, "nothing to prune") {
		t.Fatalf("output should NOT contain 'nothing to prune' when git reports work:\n%s", output)
	}
}

func TestPruneMergedEntriesFetchFailure(t *testing.T) {
	container := t.TempDir()
	gitDir, _ := seedBareRepoAt(t, container)
	worktreePath := filepath.Join(container, "worktrees", "feature")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "-b", "feature", worktreePath, "main")
	runGit(t, "", "--git-dir", gitDir, "remote", "add", "origin", "https://example.invalid/owner/repo.git")

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "fetch") {
			cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
			cmdArgs = append(cmdArgs, args...)
			cmd := exec.Command(os.Args[0], cmdArgs...)
			cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_COMMAND_EXIT=1")
			return cmd
		}
		return exec.Command(name, args...)
	}

	_, err := pruneMergedEntries(store)
	if err == nil {
		t.Fatal("pruneMergedEntries() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "fetch remote updates before prune") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "fetch remote updates before prune")
	}
}

func TestPruneMergedEntriesDefaultBaseRefFailure(t *testing.T) {
	container := t.TempDir()
	gitDir := filepath.Join(container, ".bare")
	worktreePath := filepath.Join(container, "worktrees", "feature")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, ".git"), []byte("gitdir: test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		if slices.Contains(args, "config") || slices.Contains(args, "symbolic-ref") || slices.Contains(args, "rev-parse") {
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		}
		cmd.Env = env
		return cmd
	}

	_, err := pruneMergedEntries(store)
	if err == nil {
		t.Fatal("pruneMergedEntries() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "could not determine default branch") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "could not determine default branch")
	}
}

func TestWorktreeCleanPropagatesStatusError(t *testing.T) {
	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "1")

	clean, err := worktreeClean(t.TempDir())
	if err == nil {
		t.Fatal("worktreeClean() error = nil, want error")
	}
	if clean {
		t.Fatal("worktreeClean() clean = true, want false")
	}
}

func TestGithubPRMergedOutcomes(t *testing.T) {
	entry := repoEntry{Kind: "pr", Name: "12", Path: t.TempDir()}

	t.Run("gh unavailable", func(t *testing.T) {
		defer stubExecLookPath(t, func(string) (string, error) {
			return "", exec.ErrNotFound
		})()

		merged, ok := githubPRMerged(entry)
		if merged || ok {
			t.Fatalf("githubPRMerged() = %v, %v; want false, false", merged, ok)
		}
	})

	t.Run("invalid number", func(t *testing.T) {
		defer stubExecLookPath(t, func(string) (string, error) {
			return "/usr/bin/gh", nil
		})()

		merged, ok := githubPRMerged(repoEntry{Kind: "pr", Name: "nope", Path: t.TempDir()})
		if merged || ok {
			t.Fatalf("githubPRMerged() = %v, %v; want false, false", merged, ok)
		}
	})

	t.Run("gh view failure", func(t *testing.T) {
		defer stubExecLookPath(t, func(string) (string, error) {
			return "/usr/bin/gh", nil
		})()
		defer stubExecCommand(t)()
		t.Setenv("GG_TEST_COMMAND_EXIT", "1")

		merged, ok := githubPRMerged(entry)
		if merged || ok {
			t.Fatalf("githubPRMerged() = %v, %v; want false, false", merged, ok)
		}
	})

	t.Run("merged", func(t *testing.T) {
		defer stubExecLookPath(t, func(string) (string, error) {
			return "/usr/bin/gh", nil
		})()
		defer stubExecCommand(t)()
		t.Setenv("GG_TEST_COMMAND_STDOUT", "2026-04-30T10:00:00Z\n")

		merged, ok := githubPRMerged(entry)
		if !merged || !ok {
			t.Fatalf("githubPRMerged() = %v, %v; want true, true", merged, ok)
		}
	})

	t.Run("not merged", func(t *testing.T) {
		defer stubExecLookPath(t, func(string) (string, error) {
			return "/usr/bin/gh", nil
		})()
		defer stubExecCommand(t)()

		merged, ok := githubPRMerged(entry)
		if merged || !ok {
			t.Fatalf("githubPRMerged() = %v, %v; want false, true", merged, ok)
		}
	})
}

func TestEntryMergedUsesGithubPRState(t *testing.T) {
	defer stubExecLookPath(t, func(string) (string, error) {
		return "/usr/bin/gh", nil
	})()
	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_STDOUT", "2026-04-30T10:00:00Z\n")

	merged, err := entryMerged(repoEntry{Kind: "pr", Name: "7", Path: t.TempDir()}, "main")
	if err != nil {
		t.Fatalf("entryMerged() error = %v", err)
	}
	if !merged {
		t.Fatal("entryMerged() = false, want true")
	}

	commands := readCommandLog(t)
	if len(commands) != 1 || !strings.Contains(commands[0], "gh\tpr\tview\t7") {
		t.Fatalf("commands = %v, want exactly gh pr view", commands)
	}
}

func TestRemoveWorktreeKeepsMissingBranch(t *testing.T) {
	container := t.TempDir()
	gitDir, _ := seedBareRepoAt(t, container)
	worktreePath := filepath.Join(container, "worktrees", "detached")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "--detach", worktreePath, "main")

	err := removeWorktree(store, repoEntry{Kind: "worktree", Name: "detached", Path: worktreePath})
	if err != nil {
		t.Fatalf("removeWorktree() error = %v", err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("detached worktree still exists: %v", err)
	}
}

func TestRemoveWorktreeBranchDeleteFailure(t *testing.T) {
	container := t.TempDir()
	gitDir, _ := seedBareRepoAt(t, container)
	worktreePath := filepath.Join(container, "worktrees", "feature")
	store := RepoStore{ContainerPath: container, GitDir: gitDir, MainPath: filepath.Join(container, "main"), Managed: true}

	runGit(t, "", "--git-dir", gitDir, "worktree", "add", "-b", "feature", worktreePath, "main")

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "branch") && slices.Contains(args, "-D") {
			cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
			cmdArgs = append(cmdArgs, args...)
			cmd := exec.Command(os.Args[0], cmdArgs...)
			cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_COMMAND_EXIT=1")
			return cmd
		}
		return exec.Command(name, args...)
	}

	err := removeWorktree(store, repoEntry{Kind: "worktree", Name: "feature", Path: worktreePath})
	if err == nil {
		t.Fatal("removeWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "delete merged branch") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "delete merged branch")
	}
}

func TestRepoHasOriginRemoteUnexpectedGitError(t *testing.T) {
	defer stubExecCommand(t)()
	t.Setenv("GG_TEST_COMMAND_EXIT", "2")

	ok, err := repoHasOriginRemote(filepath.Join(t.TempDir(), ".bare"))
	if err == nil {
		t.Fatal("repoHasOriginRemote() error = nil, want error")
	}
	if ok {
		t.Fatal("repoHasOriginRemote() ok = true, want false")
	}
	if !strings.Contains(err.Error(), "check origin remote") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "check origin remote")
	}
}

func TestEnsureDefaultBranchUpstreamSkipsMissingLocalBranch(t *testing.T) {
	store := RepoStore{GitDir: filepath.Join(t.TempDir(), ".bare"), MainPath: t.TempDir()}

	defer stubDefaultBranchGit(t, func(args []string) []string {
		if slices.Contains(args, "refs/heads/main") {
			return []string{"GG_TEST_COMMAND_EXIT=1"}
		}
		return nil
	})()

	err := ensureDefaultBranchUpstream(store, true)
	if err != nil {
		t.Fatalf("ensureDefaultBranchUpstream() error = %v", err)
	}

	for _, command := range readCommandLog(t) {
		if strings.Contains(command, "branch\t--set-upstream-to") {
			t.Fatalf("set-upstream command should not run when local branch is missing: %v", command)
		}
	}
}

func TestEnsureDefaultBranchUpstreamSetFailure(t *testing.T) {
	store := RepoStore{GitDir: filepath.Join(t.TempDir(), ".bare"), MainPath: t.TempDir()}

	defer stubDefaultBranchGit(t, func(args []string) []string {
		if slices.Contains(args, "branch") && slices.Contains(args, "--set-upstream-to=origin/main") {
			return []string{"GG_TEST_COMMAND_EXIT=1"}
		}
		return nil
	})()

	err := ensureDefaultBranchUpstream(store, true)
	if err == nil {
		t.Fatal("ensureDefaultBranchUpstream() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "set upstream") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "set upstream")
	}
}

func TestConfigureGitHubDefaultRepoErrors(t *testing.T) {
	t.Run("look path error", func(t *testing.T) {
		defer stubExecLookPath(t, func(string) (string, error) {
			return "", errors.New("permission denied")
		})()

		err := configureGitHubDefaultRepo(t.TempDir(), Repo{Owner: "owner", Name: "repo"})
		if err == nil {
			t.Fatal("configureGitHubDefaultRepo() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "find gh CLI") {
			t.Fatalf("error = %q, want substring %q", err.Error(), "find gh CLI")
		}
	})

	t.Run("set default failure", func(t *testing.T) {
		defer stubExecLookPath(t, func(string) (string, error) {
			return "/usr/bin/gh", nil
		})()
		defer stubExecCommand(t)()
		t.Setenv("GG_TEST_COMMAND_EXIT", "1")

		err := configureGitHubDefaultRepo(t.TempDir(), Repo{Owner: "owner", Name: "repo"})
		if err == nil {
			t.Fatal("configureGitHubDefaultRepo() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "set gh default repo") {
			t.Fatalf("error = %q, want substring %q", err.Error(), "set gh default repo")
		}
	})
}

func TestEnsureRequestPRBranch(t *testing.T) {
	cfg := setupTestConfig(t)
	container := filepath.Join(cfg.Root, cfg.Host, "owner", "repo")
	_, _ = seedBareRepoAt(t, container)

	request := Request{
		Repo:   Repo{Owner: "owner", Name: "repo"},
		Target: Target{Kind: TargetPR, PRNumber: 7},
	}

	defer stubExecCommandExcept(t, "git")()

	path, err := ensureRequest(cfg, request)
	if err != nil {
		t.Fatalf("ensureRequest() error = %v", err)
	}
	wantPath := filepath.Join(container, "PR", "7")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
}

func TestDiscoverEntriesSkipsNonDirAndBareDirs(t *testing.T) {
	root := t.TempDir()

	// A regular file at the root — hits the !d.IsDir branch.
	if err := os.WriteFile(filepath.Join(root, "loose-file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// A directory without a .git sentinel — hits the !gitExists branch.
	if err := os.MkdirAll(filepath.Join(root, "not-a-worktree"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	// A directory that IS a worktree — should be the only entry returned.
	realPath := filepath.Join(root, "real")
	if err := os.MkdirAll(realPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(realPath, ".git"), []byte("gitdir: x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	entries, err := discoverEntries(root, "worktree")
	if err != nil {
		t.Fatalf("discoverEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (%+v)", len(entries), entries)
	}
	if entries[0].Name != "real" {
		t.Fatalf("entries[0].Name = %q, want real", entries[0].Name)
	}
}

func TestRemoveEmptyChildrenSkipsFiles(t *testing.T) {
	root := t.TempDir()

	// Loose file — must be skipped (not directory), must survive.
	filePath := filepath.Join(root, "loose.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// Empty directory — should be removed.
	emptyDir := filepath.Join(root, "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	removed, err := removeEmptyChildren(root)
	if err != nil {
		t.Fatalf("removeEmptyChildren() error = %v", err)
	}
	if len(removed) != 1 || removed[0] != emptyDir {
		t.Fatalf("removed = %v, want [%q]", removed, emptyDir)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("loose file should survive, got error %v", err)
	}
	if _, err := os.Stat(emptyDir); !os.IsNotExist(err) {
		t.Fatalf("empty directory should be removed, got error %v", err)
	}
}

func TestFinalizeWorktreeSetupMisePropagates(t *testing.T) {
	worktree := t.TempDir()
	// Presence of mise.toml ensures setupMiseTooling attempts runCommand
	// (mise trust …). That's where we inject failure.
	if err := os.WriteFile(filepath.Join(worktree, "mise.toml"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		// Only fail mise commands — submodule update must succeed so that
		// finalizeWorktreeSetup proceeds to the setupMiseTooling step.
		if name == "mise" {
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		}
		cmd.Env = env
		return cmd
	}

	err := finalizeWorktreeSetup(worktree, Repo{})
	if err == nil {
		t.Fatal("finalizeWorktreeSetup() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "trust mise config") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "trust mise config")
	}
}

func TestEnsureRepoStoreRepoHasRefsError(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	// Pre-create the bare directory so the clone step is skipped, letting
	// ensureRepoStore proceed to the repoHasRefs probe.
	bare := repo.BarePath(cfg)
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		// for-each-ref is the call repoHasRefs makes; force a non-zero exit
		// so captureCommand returns an error wrapped by repoHasRefs.
		if slices.Contains(args, "for-each-ref") {
			env = append(env, "GG_TEST_COMMAND_EXIT=1")
		}
		cmd.Env = env
		return cmd
	}

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "check refs for") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "check refs for")
	}
}

func TestEnsureRepoStoreLocalBranchExistsError(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GG_TEST_SIMULATE_MKDIR=1")
		hasLocalHeadsProbe := false
		for _, a := range args {
			if strings.HasPrefix(a, "refs/heads/") {
				hasLocalHeadsProbe = true
				break
			}
		}
		switch {
		case slices.Contains(args, "for-each-ref"):
			env = append(env, "GG_TEST_COMMAND_STDOUT=refs/heads/main")
		case slices.Contains(args, "symbolic-ref"):
			env = append(env, "GG_TEST_COMMAND_STDOUT=origin/main")
		case hasLocalHeadsProbe:
			// Non-1 exit code surfaces as a wrapped error from localBranchExists.
			env = append(env, "GG_TEST_COMMAND_EXIT=2")
		}
		cmd.Env = env
		return cmd
	}

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "check branch") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "check branch")
	}
}

// Seam-driven tests: inject failures into package-level OS seams and assert the
// call site's wrapping prefix. Each test covers a distinct wrap site so the
// error message is a reliable witness that the intended branch ran.

func TestLoadConfigReadFileError(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("GG_CONFIG", cfgPath)
	t.Setenv("HOME", t.TempDir())

	oldReadFile := osReadFile
	defer func() { osReadFile = oldReadFile }()
	osReadFile = func(string) ([]byte, error) {
		return nil, errors.New("simulated read failure")
	}

	_, _, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "read config")
	}
}

func TestEnsureRepoStoreMkdirAllError(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "repo"}

	oldMkdirAll := osMkdirAll
	defer func() { osMkdirAll = oldMkdirAll }()
	osMkdirAll = func(string, os.FileMode) error {
		return errors.New("simulated mkdir failure")
	}

	_, err := ensureRepoStore(cfg, repo)
	if err == nil {
		t.Fatal("ensureRepoStore() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create repository container") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "create repository container")
	}
}

func TestEnsureWorktreeMkdirAllError(t *testing.T) {
	store := RepoStore{
		ContainerPath: t.TempDir(),
		GitDir:        filepath.Join(t.TempDir(), ".bare"),
		MainPath:      filepath.Join(t.TempDir(), "main"),
		Managed:       true,
	}

	oldMkdirAll := osMkdirAll
	defer func() { osMkdirAll = oldMkdirAll }()
	osMkdirAll = func(string, os.FileMode) error {
		return errors.New("simulated mkdir failure")
	}

	_, err := ensureWorktree(store, "feature")
	if err == nil {
		t.Fatal("ensureWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create worktree parent directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "create worktree parent directory")
	}
}

func TestEnsurePRWorktreeMkdirAllError(t *testing.T) {
	store := RepoStore{
		ContainerPath: t.TempDir(),
		GitDir:        filepath.Join(t.TempDir(), ".bare"),
		MainPath:      filepath.Join(t.TempDir(), "main"),
		Managed:       true,
	}

	oldMkdirAll := osMkdirAll
	defer func() { osMkdirAll = oldMkdirAll }()
	osMkdirAll = func(string, os.FileMode) error {
		return errors.New("simulated mkdir failure")
	}

	_, err := ensurePRWorktree(store, 42)
	if err == nil {
		t.Fatal("ensurePRWorktree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create PR parent directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "create PR parent directory")
	}
}

func TestEnsureOwnerPathMkdirAllError(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}

	oldMkdirAll := osMkdirAll
	defer func() { osMkdirAll = oldMkdirAll }()
	osMkdirAll = func(string, os.FileMode) error {
		return errors.New("simulated mkdir failure")
	}

	_, err := ensureOwnerPath(cfg, "owner")
	if err == nil {
		t.Fatal("ensureOwnerPath() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create owner directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "create owner directory")
	}
}

func TestInitConfigCommandMkdirAllError(t *testing.T) {
	t.Setenv("GG_CONFIG", filepath.Join(t.TempDir(), "gg", "config"))
	t.Setenv("HOME", t.TempDir())

	oldMkdirAll := osMkdirAll
	defer func() { osMkdirAll = oldMkdirAll }()
	osMkdirAll = func(string, os.FileMode) error {
		return errors.New("simulated mkdir failure")
	}

	err := initConfigCommand()
	if err == nil {
		t.Fatal("initConfigCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create config directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "create config directory")
	}
}

func TestWriteConfigMkdirAllError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gg", "config")

	oldMkdirAll := osMkdirAll
	defer func() { osMkdirAll = oldMkdirAll }()
	osMkdirAll = func(string, os.FileMode) error {
		return errors.New("simulated mkdir failure")
	}

	err := writeConfig(path, Config{Root: "/tmp", Host: "github.com"})
	if err == nil {
		t.Fatal("writeConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create config directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "create config directory")
	}
}

func TestDirectoryExistsStatError(t *testing.T) {
	oldStat := osStat
	defer func() { osStat = oldStat }()
	osStat = func(string) (os.FileInfo, error) {
		return nil, errors.New("simulated stat failure")
	}

	_, err := directoryExists("/some/path")
	if err == nil {
		t.Fatal("directoryExists() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "stat /some/path") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "stat /some/path")
	}
}

func TestPathExistsStatError(t *testing.T) {
	oldStat := osStat
	defer func() { osStat = oldStat }()
	osStat = func(string) (os.FileInfo, error) {
		return nil, errors.New("simulated stat failure")
	}

	_, err := pathExists("/some/path")
	if err == nil {
		t.Fatal("pathExists() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "stat /some/path") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "stat /some/path")
	}
}

func TestInitConfigCommandStatError(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "gg", "config")
	t.Setenv("GG_CONFIG", cfgPath)
	t.Setenv("HOME", t.TempDir())

	oldStat := osStat
	defer func() { osStat = oldStat }()
	osStat = func(path string) (os.FileInfo, error) {
		if path == cfgPath {
			return nil, errors.New("simulated stat failure")
		}
		return os.Stat(path)
	}

	err := initConfigCommand()
	if err == nil {
		t.Fatal("initConfigCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "stat config") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "stat config")
	}
}

func TestInitConfigCommandWriteFileError(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "gg", "config")
	t.Setenv("GG_CONFIG", cfgPath)
	t.Setenv("HOME", t.TempDir())

	oldWriteFile := osWriteFile
	defer func() { osWriteFile = oldWriteFile }()
	osWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("simulated write failure")
	}

	err := initConfigCommand()
	if err == nil {
		t.Fatal("initConfigCommand() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "write config") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "write config")
	}
}

func TestWriteConfigWriteFileError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")

	oldWriteFile := osWriteFile
	defer func() { osWriteFile = oldWriteFile }()
	osWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("simulated write failure")
	}

	err := writeConfig(path, Config{Root: "/tmp", Host: "github.com"})
	if err == nil {
		t.Fatal("writeConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "write config") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "write config")
	}
}

func TestClassifyExistingRepoPathReadDirError(t *testing.T) {
	container := t.TempDir()
	store := RepoStore{
		ContainerPath: container,
		GitDir:        filepath.Join(container, ".bare"),
		MainPath:      filepath.Join(container, "main"),
		Managed:       true,
	}

	oldReadDir := osReadDir
	defer func() { osReadDir = oldReadDir }()
	osReadDir = func(string) ([]os.DirEntry, error) {
		return nil, errors.New("simulated readdir failure")
	}

	_, err := classifyExistingRepoPath(store)
	if err == nil {
		t.Fatal("classifyExistingRepoPath() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "read repository directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "read repository directory")
	}
}

func TestRemoveEmptyChildrenReadDirError(t *testing.T) {
	root := t.TempDir()

	oldReadDir := osReadDir
	defer func() { osReadDir = oldReadDir }()
	osReadDir = func(string) ([]os.DirEntry, error) {
		return nil, errors.New("simulated readdir failure")
	}

	_, err := removeEmptyChildren(root)
	if err == nil {
		t.Fatal("removeEmptyChildren() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "read directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "read directory")
	}
}

func TestRemoveEmptyTreeReadDirErrorFirstCall(t *testing.T) {
	dir := t.TempDir()

	oldReadDir := osReadDir
	defer func() { osReadDir = oldReadDir }()
	osReadDir = func(string) ([]os.DirEntry, error) {
		return nil, errors.New("simulated readdir failure")
	}

	_, err := removeEmptyTree(dir)
	if err == nil {
		t.Fatal("removeEmptyTree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "read directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "read directory")
	}
}

func TestRemoveEmptyTreeReadDirErrorSecondCall(t *testing.T) {
	dir := t.TempDir()

	oldReadDir := osReadDir
	defer func() { osReadDir = oldReadDir }()
	call := 0
	osReadDir = func(path string) ([]os.DirEntry, error) {
		call++
		if call == 1 {
			// First call drives the recursion loop — return no entries so it
			// exits cleanly and control reaches the second read.
			return nil, nil
		}
		return nil, errors.New("simulated readdir failure on second call")
	}

	_, err := removeEmptyTree(dir)
	if err == nil {
		t.Fatal("removeEmptyTree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "read directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "read directory")
	}
	if call < 2 {
		t.Fatalf("osReadDir call count = %d, want >= 2", call)
	}
}

func TestRemoveEmptyTreeRemoveError(t *testing.T) {
	dir := t.TempDir()

	oldRemove := osRemove
	defer func() { osRemove = oldRemove }()
	osRemove = func(string) error {
		return errors.New("simulated remove failure")
	}

	_, err := removeEmptyTree(dir)
	if err == nil {
		t.Fatal("removeEmptyTree() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "remove directory") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "remove directory")
	}
}

func TestDiscoverEntriesWalkDirError(t *testing.T) {
	root := t.TempDir()

	oldWalk := filepathWalkDir
	defer func() { filepathWalkDir = oldWalk }()
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error {
		_ = fn
		return errors.New("simulated walk failure")
	}

	_, err := discoverEntries(root, "worktree")
	if err == nil {
		t.Fatal("discoverEntries() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "scan ") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "scan ")
	}
}

func TestDiscoverEntriesDirectoryExistsError(t *testing.T) {
	root := t.TempDir()

	oldStat := osStat
	defer func() { osStat = oldStat }()
	osStat = func(path string) (os.FileInfo, error) {
		if path == root {
			return nil, errors.New("simulated stat failure")
		}
		return os.Stat(path)
	}

	_, err := discoverEntries(root, "worktree")
	if err == nil {
		t.Fatal("discoverEntries() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "stat "+root) {
		t.Fatalf("error = %q, want substring %q", err.Error(), "stat "+root)
	}
}

func TestRemoveEmptyChildrenDirectoryExistsError(t *testing.T) {
	root := t.TempDir()

	oldStat := osStat
	defer func() { osStat = oldStat }()
	osStat = func(path string) (os.FileInfo, error) {
		if path == root {
			return nil, errors.New("simulated stat failure")
		}
		return os.Stat(path)
	}

	_, err := removeEmptyChildren(root)
	if err == nil {
		t.Fatal("removeEmptyChildren() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "stat "+root) {
		t.Fatalf("error = %q, want substring %q", err.Error(), "stat "+root)
	}
}
