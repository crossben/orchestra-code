package main

import "fmt"

func errDirty() error {
	return fmt.Errorf("working tree has uncommitted changes; the supervised loop " +
		"reverts on reject and would discard them — commit/stash first, or pass --force")
}
