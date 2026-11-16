package cli

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
)

// clearLine returns the cursor to the line's start and erases the line.
const clearLine = "\r\x1b[2K"

// unknownWidth is the width a progress line is cut to when the terminal's is not known.
const unknownWidth = 80

// pace is how a progress line draws: nothing until delay has passed since the run began (or since
// other output last held the line), so a quick run shows nothing, and then no more often than
// every.
type pace struct{ delay, every time.Duration }

// progress shows what a slow step is working on, on stderr where a person waits, so a long run
// does not look hung. It returns the engine.Progress that reports the steps and the function that
// clears what was shown, which a caller defers over the engine call it reports on. Both do
// nothing under --json or --plain, or unless stderr is a terminal, so scripts and test buffers see
// no extra output. In accessible mode (no cursor control) every step is a line of its own, at
// once. On a terminal that redraws, each step replaces the last in one line, drawn at the app's
// pace; a held step (a script, a hook) clears the line and draws nothing, and a kept one (a fetch
// over ssh, which may ask on the terminal) is printed at once as a line that stays; either way
// nothing draws until the next step.
//
// Nothing else is written over the line: every form and question (huhForms.runIn, app.ask), every
// printed or paged report (app.pageThen, which app.emit prints through), and the engine callbacks
// that print mid-run without asking (app.receipt under -y or off a terminal, ShowResolved) hold
// it first (app.holdProgress), and a later step draws it again once the delay has passed.
func (a *app) progress(cmd *cobra.Command) (engine.Progress, func()) {
	out := cmd.ErrOrStderr()
	if a.json || a.plain || !a.termErr && !isTTY(out) {
		return engine.Progress{}, func() {}
	}
	if a.accessible() {
		line := func(step string) { fmt.Fprintln(out, "… "+step) }
		return engine.Progress{Step: line, Hold: func(step string, _ bool) { line(step) }}, func() {}
	}
	p := &progressLine{out: out, width: func() int { return a.width(out) }, pace: a.pace, since: time.Now()}
	a.line = p
	return engine.Progress{Step: p.report, Hold: p.hold}, func() {
		p.hold("", false)
		if a.line == p {
			a.line = nil
		}
	}
}

// holdProgress clears the progress line, when there is one, before other output reaches the
// terminal, and returns what the caller runs once that output is done (a question answered, a
// report closed): the next step then waits out the delay again.
func (a *app) holdProgress() (resume func()) {
	p := a.line
	if p == nil {
		return func() {}
	}
	p.hold("", false)
	return p.resume
}

// progressLine is the one line a run's progress redraws (app.progress). A step reported before
// the pace allows a draw is drawn by a timer once it does, unless a later step replaces it or the
// line is held first.
type progressLine struct {
	out   io.Writer
	width func() int // the terminal's width, read at each draw; 0 when unknown
	pace  pace

	mu    sync.Mutex
	since time.Time // nothing is drawn before since+pace.delay
	drawn time.Time // the last draw; the next is pace.every after it
	step  string    // the step waiting to be drawn, "" for none
	shown bool      // a step is on the line
	timer *time.Timer
	gen   int // which timer may draw: holding or arming another retires the last
}

// report makes step the one the line shows: at once when the pace allows, else when it does.
func (p *progressLine) report(step string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.step = step
	now := time.Now()
	at := p.since.Add(p.pace.delay)
	if next := p.drawn.Add(p.pace.every); next.After(at) {
		at = next
	}
	if wait := at.Sub(now); wait > 0 {
		if p.timer == nil {
			p.gen++
			gen := p.gen
			p.timer = time.AfterFunc(wait, func() { p.fire(gen) })
		}
		return
	}
	p.draw(now)
}

// fire draws the waiting step for the timer of generation gen, unless that timer was retired.
func (p *progressLine) fire(gen int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != p.gen {
		return
	}
	p.timer = nil
	if p.step != "" {
		p.draw(time.Now())
	}
}

// draw replaces the line with the waiting step, cut to the terminal's width. p.mu is held.
func (p *progressLine) draw(now time.Time) {
	width := p.width()
	if width <= 0 {
		width = unknownWidth
	}
	fmt.Fprint(p.out, clearLine+ansi.Truncate("… "+p.step, width-1, "…"))
	p.step, p.shown, p.drawn = "", true, now
}

// hold erases the line, drops the step waiting and its timer, and, when keep is set, prints step
// as a line of its own that stays; nothing draws until the next step, which waits out the delay.
func (p *progressLine) hold(step string, keep bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.gen++
	p.step = ""
	if p.shown {
		fmt.Fprint(p.out, clearLine)
		p.shown = false
	}
	if keep {
		fmt.Fprintln(p.out, "… "+step)
	}
	p.since = time.Now()
}

// resume starts the delay again once the output that held the line is done.
func (p *progressLine) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.since = time.Now()
}
