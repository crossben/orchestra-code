package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/crossben/orchestra-code/internal/validate"
)

// stagesFor resolves the validation stages for the working dir, announcing when
// they were auto-detected so the user knows what will run.
func stagesFor(cfg *config.Config) []validate.Stage {
	stages, detected := cfg.ResolveStages(flagDir)
	if detected && len(stages) > 0 {
		names := make([]string, len(stages))
		for i, s := range stages {
			names[i] = s.Name
		}
		fmt.Printf("%s auto-detected checks: %s\n", ui.Accent("▸"), ui.Dim(strings.Join(names, " → ")))
	}
	return stages
}

// absDir returns the absolute form of the --dir flag, used as the memory key so
// history is stable regardless of where orchestra is invoked from.
func absDir() (string, error) {
	return filepath.Abs(flagDir)
}

// openMemory opens the shared memory store (~/.orchestra/orchestra.db).
func openMemory() (*memory.Store, error) {
	path, err := memory.DefaultPath()
	if err != nil {
		return nil, err
	}
	return memory.Open(path)
}
