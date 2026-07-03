package main

import "fmt"

func errNotRepo(dir string) error {
	return fmt.Errorf("%s is not a git repository (run `git init` first)", dir)
}

func errDirty() error {
	return fmt.Errorf("working tree has uncommitted changes; the supervised loop " +
		"reverts on reject and would discard them — commit/stash first, or pass --force")
}
