package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyExistingRepoPathEmptyIsLocal(t *testing.T) {
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
	if got != "local" {
		t.Fatalf("classifyExistingRepoPath() = %q, want %q", got, "local")
	}
}

func TestEnsureRepoStoreReturnsEmptyLocalDirectory(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Host: "github.com"}
	repo := Repo{Owner: "owner", Name: "empty"}
	localPath := repo.ContainerPath(cfg)
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}
	if store.Managed {
		t.Fatal("store.Managed = true, want false for empty local directory")
	}
	if store.MainPath != localPath {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, localPath)
	}
}
