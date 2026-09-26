package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/router"
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

// The memory store is the router's track-record source.
var _ router.History = (*memory.Store)(nil)

// withHistory makes r history-aware when the memory store opened. A nil store
// is skipped (not wrapped as a typed-nil interface), keeping routing as before.
func withHistory(r *router.Router, mem *memory.Store) *router.Router {
	if mem == nil {
		return r
	}
	return r.WithHistory(mem)
}

// openMemory opens the shared memory store (~/.orchestra/orchestra.db).
func openMemory() (*memory.Store, error) {
	path, err := memory.DefaultPath()
	if err != nil {
		return nil, err
	}
	return memory.Open(path)
}
