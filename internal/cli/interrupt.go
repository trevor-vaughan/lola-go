package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// Interrupted is the cause of a run's context cancelled by SIGINT or SIGTERM: the command stops
// what it is waiting on (a fetch's git, a download, a hook), its deferred cleanup removes its
// staging, and Execute exits 128 plus the signal's number, as the signal itself would have.
type Interrupted struct{ Signal syscall.Signal }

func (i Interrupted) Error() string { return i.Signal.String() }

// NotifyContext returns parent cancelled, with an Interrupted cause, by the first SIGINT or
// SIGTERM. That signal then has its default effect again, so a second one ends lola at once.
// SIGINT lola was started with ignored (a background job of a non-interactive shell) stays
// ignored; SIGTERM never reads as ignored, since Go installs its own handler for it at start.
// Call stop when the run ends.
func NotifyContext(parent context.Context) (ctx context.Context, stop context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	var heard []os.Signal
	for _, s := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		if !signal.Ignored(s) {
			heard = append(heard, s)
		}
	}
	if len(heard) == 0 {
		return ctx, func() { cancel(nil) } // Notify with no signals would take every signal
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, heard...)
	go func() {
		select {
		case s := <-sigs:
			cancel(Interrupted{Signal: s.(syscall.Signal)}) // both signals Notify listens for are syscall.Signal
		case <-ctx.Done():
		}
		signal.Stop(sigs)
	}()
	return ctx, func() { cancel(nil) }
}

// cancelled reports whether err is ctx's cancel by anything but a signal, which a command takes as
// a cancelled question. A signal's cancel stays the run's error, so Execute exits as the signal
// would.
func cancelled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) && !errors.As(context.Cause(ctx), new(Interrupted))
}
