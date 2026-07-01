package ui

import (
	"fmt"
	"sync"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner animates a single status line during phases where nothing else is
// printing (classification, planning, answering). It is a no-op when rich output
// is disabled, so piped/CI runs stay clean.
type Spinner struct {
	msg  string
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Spin starts a spinner with the given message. Always call Stop (defer it).
func Spin(msg string) *Spinner {
	s := &Spinner{msg: msg, stop: make(chan struct{}), done: make(chan struct{})}
	if !Enabled {
		close(s.done)
		return s
	}
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer close(s.done)
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	i := 0
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			frame := styAccent2.Render(spinnerFrames[i%len(spinnerFrames)])
			fmt.Printf("\r  %s %s", frame, styDim.Render(s.msg))
			i++
		}
	}
}

// Stop halts the spinner and clears its line.
func (s *Spinner) Stop() {
	s.once.Do(func() {
		if Enabled {
			close(s.stop)
		}
	})
	<-s.done
	if Enabled {
		fmt.Print("\r\033[K") // carriage return + clear to end of line
	}
}
