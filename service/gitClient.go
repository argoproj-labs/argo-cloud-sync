package main

import (
	"fmt"

	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
)

type gitClient interface {
	CheckoutFileFromRepository(repository, commitHash, path string) ([]byte, error)
}

type basicGitClient struct {
	auth *ssh.PublicKeys
}

func newBasicGitClient(sshPemFile string) (basicGitClient, error) {
	auth, err := ssh.NewPublicKeysFromFile("git", sshPemFile, "")
	if err != nil {
		return basicGitClient{}, err
	}

	return basicGitClient{
		auth: auth,
	}, nil
}

func (g basicGitClient) CheckoutFileFromRepository(repository, commitHash, path string) ([]byte, error) {
	// TODO: refactor to not use mem-backed
	storer := memory.NewStorage()
	fs := memfs.New()

	// TODO: use context version and make depth configurable
	repo, err := git.Clone(storer, fs, &git.CloneOptions{
		URL:  repository,
		Auth: g.auth,
	})
	if err != nil {
		return []byte{}, err
	}

	w, err := repo.Worktree()
	if err != nil {
		return []byte{}, err
	}

	err = w.Checkout(&git.CheckoutOptions{
		Hash: plumbing.NewHash(commitHash),
	})
	if err != nil {
		return []byte{}, err
	}

	fileStat, err := fs.Stat(path)
	if err != nil {
		return []byte{}, err
	}

	if fileStat.IsDir() {
		return []byte{}, fmt.Errorf("path provided is not a file '%s'", path)
	}

	file, err := fs.Open(path)
	if err != nil {
		return []byte{}, err
	}

	fileContents := make([]byte, fileStat.Size())
	_, err = file.Read(fileContents)

	return fileContents, err
}
