package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	newTemplateRepoURL = "https://github.com/puria/md.git"
	initialCommitMsg   = "feat: Initial commit 🎉 by [gg](https://github.com/puria/gg)"
)

var osMkdirTemp = os.MkdirTemp //nolint:gochecknoglobals

var osRemoveAll = os.RemoveAll //nolint:gochecknoglobals

func newCommand(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: gg new <owner/repo>")
	}

	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}

	repo, err := resolveOneArg(cfg, args[0])
	if err != nil {
		return err
	}

	repoPath := repo.ContainerPath(cfg)
	exists, err := pathExists(repoPath)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("repository already exists: %s", repoPath)
	}

	if err := osMkdirAll(repoPath, 0o755); err != nil {
		return fmt.Errorf("create repository directory %s: %w", repoPath, err)
	}

	templatePath, err := osMkdirTemp("", "gg-md-*")
	if err != nil {
		return fmt.Errorf("create template download directory: %w", err)
	}
	defer func() {
		_ = osRemoveAll(templatePath)
	}()

	if err := runCommand("", "git", "clone", "--depth", "1", newTemplateRepoURL, templatePath); err != nil {
		return fmt.Errorf("download markdown templates: %w", err)
	}

	if err := copyMarkdownFiles(templatePath, repoPath); err != nil {
		return err
	}

	if err := runCommand(repoPath, "git", "init"); err != nil {
		return fmt.Errorf("initialize git repository: %w", err)
	}
	if err := runCommand(repoPath, "git", "add", "--all"); err != nil {
		return fmt.Errorf("stage initial files: %w", err)
	}
	if err := runCommand(repoPath, "git", "commit", "-m", initialCommitMsg); err != nil {
		return fmt.Errorf("create initial commit: %w", err)
	}

	fmt.Println(repoPath)
	return nil
}

func copyMarkdownFiles(srcRoot, dstRoot string) error {
	copied := 0
	err := filepath.WalkDir(srcRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}

		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return fmt.Errorf("resolve template path %s: %w", path, err)
		}
		if err := copyFile(path, filepath.Join(dstRoot, rel)); err != nil {
			return err
		}
		copied++
		return nil
	})
	if err != nil {
		return fmt.Errorf("copy markdown templates: %w", err)
	}
	if copied == 0 {
		return fmt.Errorf("copy markdown templates: no markdown files found in %s", newTemplateRepoURL)
	}

	return nil
}

func copyFile(src, dst string) error {
	input, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open template file %s: %w", src, err)
	}
	defer func() {
		_ = input.Close()
	}()

	if err := osMkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create template directory %s: %w", filepath.Dir(dst), err)
	}

	output, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create template file %s: %w", dst, err)
	}
	defer func() {
		_ = output.Close()
	}()

	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copy template file %s: %w", src, err)
	}

	return nil
}
