package main

import (
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
)

// installTerminationHandler forwards termination aimed at the launcher to the
// private runtime it owns, so an operator or supervisor that stops the launcher
// never leaves a private Node process behind. The returned function stops
// handling and releases the handler goroutine.
func installTerminationHandler(l *Launcher) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, terminationSignals()...)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				_ = l.Close()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(ch)
			close(done)
		})
	}
}

// terminationSignals are the signals that mean "stop the runtime too".
func terminationSignals() []os.Signal {
	if runtime.GOOS == "windows" {
		return []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
