// Package review presents the agent's diff to the human and asks for a decision.
// This is the "supervised" in supervised-first: nothing is kept without an
// explicit accept.
package review

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/crossben/orchestra/internal/validate"
)

// Prompt shows the diff and validation outcome, then asks the human to accept
// or reject. Returns true to keep the changes. The reader is shared with the
// caller (e.g. the interactive shell) so buffered input isn't split across two
// readers.
//
// If input is not a terminal (e.g. piped/CI), it defaults to reject — the safe
// choice, since accept is the irreversible one.
func Prompt(reader *bufio.Reader, diff string, v validate.Result) bool {
	fmt.Println()
	fmt.Println("──────────────── proposed changes ────────────────")
	fmt.Println(diff)
	fmt.Println("───────────────────────────────────────────────────")

	switch {
	case v.Skipped:
		fmt.Println("validation: skipped (unverified)")
	case v.Passed:
		fmt.Println("validation: ✓ passed")
	default:
		fmt.Println("validation: ✗ FAILED")
	}

	for {
		fmt.Print("accept these changes? [y/N] ")
		line, err := reader.ReadString('\n')
		if err != nil {
			// EOF / non-interactive input: reject by default.
			fmt.Println()
			return false
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		case "", "n", "no":
			return false
		default:
			fmt.Println("please answer y or n")
		}
	}
}
