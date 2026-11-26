package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// isTTY reports whether a stream is a terminal. Test buffers never are, which keeps output
// deterministic.
func isTTY(s any) bool {
	f, ok := s.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// width is how many columns human output to w may fill: the terminal's when w is one, else
// $COLUMNS when it is a positive number, else 0, no limit, so a pipe or a file keeps every
// character.
func (a *app) width(w any) int {
	if f, ok := w.(*os.File); ok {
		if cols, _, err := term.GetSize(int(f.Fd())); err == nil && cols > 0 {
			return cols
		}
	}
	if n, err := strconv.Atoi(a.getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 0
}

// height is how many rows the terminal w is on has: the terminal's when w is one, else $LINES
// when it is a positive number, else 0, unknown, which never opens the viewer.
func (a *app) height(w any) int {
	if f, ok := w.(*os.File); ok {
		if _, rows, err := term.GetSize(int(f.Fd())); err == nil && rows > 0 {
			return rows
		}
	}
	if n, err := strconv.Atoi(a.getenv("LINES")); err == nil && n > 0 {
		return n
	}
	return 0
}

// style renders human output: glyphs always, ANSI color only when enabled, bold only when bold is
// set (app.bold), nothing fancy under --plain. paged is set when the output goes to the viewer or a
// pager, which shows all of it, so printers drop their caps (more). verbose is -v: a run's detail
// notices are printed, not counted (printNotices); a command without -v prints them all, as it did
// before -v.
// asking marks a plan shown just before the question that applies it, so it is no dry run.
// width is the columns a hint wraps to (hintLine): the terminal's when stdout and stderr both are
// one, else 0, so a pipe or a file gets each hint on one line.
type style struct {
	color, bold, plain, paged, verbose, asking, applied bool
	width                                               int
	// shown collects the shown record keys of the consent items the output prints in full (saw),
	// which emit adds to the record once it has printed them; nil collects nothing.
	shown *[]string
}

// saw notes keys, those of consent items the output prints in full, for emit to record.
func (s style) saw(keys ...string) {
	if s.shown != nil {
		*s.shown = append(*s.shown, keys...)
	}
}

// more is the command a capped listing names to list everything, or "" when the output is paged
// and shows everything.
func (s style) more(cmd string) string {
	if s.paged {
		return ""
	}
	return cmd
}

func (a *app) style(cmd *cobra.Command) style {
	if a.plain {
		return style{plain: true}
	}
	s := style{color: a.color(cmd.OutOrStdout()), bold: a.bold(cmd.OutOrStdout()), verbose: cmd.Flags().Lookup("verbose") == nil || given(cmd, "verbose")}
	if (a.termOut || isTTY(cmd.OutOrStdout())) && (a.termErr || isTTY(cmd.ErrOrStderr())) {
		s.width = a.width(cmd.OutOrStdout())
	}
	return s
}

// color decides ANSI color for output to w: LOLA_COLOR, then NO_COLOR, then whether w is a
// terminal. The install flow's forms on stderr follow the same rule.
func (a *app) color(w any) bool {
	switch a.getenv("LOLA_COLOR") {
	case "1":
		return true
	case "0":
		return false
	}
	return a.getenv("NO_COLOR") == "" && isTTY(w)
}

// bold decides bold emphasis for output to w, which is not color: NO_COLOR and LOLA_COLOR=0 keep
// it on a terminal. LOLA_COLOR=1 forces it; --plain, TERM=dumb and a w that is no terminal drop it.
func (a *app) bold(w any) bool {
	switch {
	case a.plain:
		return false
	case a.getenv("LOLA_COLOR") == "1":
		return true
	}
	return a.getenv("TERM") != "dumb" && isTTY(w)
}

// emit is the one output path: JSON under --json, the run's contexts first (withContext) and its
// notes in its notices (withNotices), otherwise the human renderer, through page, which opens the
// viewer when the output is taller than the terminal, then the notes on stderr (printNotes).
func (a *app) emit(cmd *cobra.Command, human func(io.Writer, style), v any) error {
	w := cmd.OutOrStdout()
	var shown []string
	if a.json {
		doc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if doc, err = withContext(doc, a.contexts); err != nil {
			return err
		}
		if doc, err = withNotices(doc, a.notes); err != nil {
			return err
		}
		a.notes = nil
		var out bytes.Buffer
		if err := json.Indent(&out, doc, "", "  "); err != nil {
			return err
		}
		out.WriteByte('\n')
		if _, err = w.Write(out.Bytes()); err != nil {
			return err
		}
		a.remember(cmd, shownIn(reflect.ValueOf(v)))
		return nil
	}
	s := a.style(cmd)
	s.shown = &shown
	a.page(cmd, w, s, human)
	a.printNotes(cmd)
	a.remember(cmd, shown)
	return nil
}

// remember adds keys, those of the consent items the output printed in full, to the shown record,
// which --trust's consent kinds grant from. Output with none needs no engine. The run has done
// its work by then, so a failure is a warning on stderr (none under --json, whose stderr holds an
// error alone), never the run's: the items are shown again next time.
func (a *app) remember(cmd *cobra.Command, keys []string) {
	keys = slices.DeleteFunc(keys, func(k string) bool { return k == "" })
	if len(keys) == 0 {
		return
	}
	eng, err := a.engine()
	if err == nil {
		err = eng.Shown.Add(keys...)
	}
	if err != nil && !a.json {
		w := cmd.ErrOrStderr()
		fmt.Fprintln(w, style{color: a.color(w), bold: a.bold(w)}.warn("lola could not remember what it showed you ("+
			untrusted.CleanErr(err).Error()+"); --trust consents to it only once it is shown again"))
	}
}

// shownIn is the shown record keys of every run result (engine.Result.ShownKeys) in v, a --json
// document, which prints each of their consent items in full: v's exported fields JSON keeps,
// its elements and pointers followed.
func shownIn(v reflect.Value) []string {
	if !v.IsValid() {
		return nil
	}
	if (v.Kind() != reflect.Pointer || !v.IsNil()) && v.CanInterface() {
		if r, ok := reflect.TypeAssert[interface{ ShownKeys() []string }](v); ok {
			return r.ShownKeys()
		}
	}
	var keys []string
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			keys = shownIn(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Type().Field(i); f.IsExported() && f.Tag.Get("json") != "-" {
				keys = append(keys, shownIn(v.Field(i))...)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			keys = append(keys, shownIn(v.Index(i))...)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			keys = append(keys, shownIn(it.Value())...)
		}
	}
	return keys
}

// jsonFielder is an error whose --json document carries more keys than error and hints (a sync
// refused for want of answers carries the changes and removals it needs them for).
type jsonFielder interface {
	error
	JSONFields() map[string]any
}

// jsonFields is a new map holding the keys err's --json document carries beyond error and hints:
// those of the first jsonFielder in its chain, else none.
func jsonFields(err error) map[string]any {
	fields := map[string]any{}
	if f, ok := errors.AsType[jsonFielder](err); ok {
		maps.Copy(fields, f.JSONFields())
	}
	return fields
}

// writeError prints err per the output contract, its hints wrapped to width (hintLine).
func writeError(w io.Writer, err error, asJSON bool, width int) {
	hints := errs.Hints(err)
	if hints == nil {
		hints = []string{}
	}
	if asJSON {
		doc := jsonFields(err)
		doc["error"], doc["hints"] = err.Error(), hints
		if json.NewEncoder(w).Encode(doc) == nil {
			return
		}
		// Encoding failed (or the write did): fall through to the human format.
	}
	fmt.Fprintf(w, "error: %s\n", err)
	for _, h := range hints {
		fmt.Fprintln(w, hintLine("  ", h, width))
	}
}

// minHintText is how many columns a wrapped hint or notice keeps for its text after its lead: on a
// narrower terminal it prints as one line, which the terminal wraps.
const minHintText = 20

// hintLine is the "hint:" line for h after indent, wrapped to width columns (wrapAt).
func hintLine(indent, h string, width int) string {
	return wrapAt(indent+"hint: ", h, width, false)
}

// wrapAt is lead then text, wrapped at spaces to width columns (0 for no limit), each continuation
// under the text's first word. Unless prose, a command in text, from a word "lola" to a word ending
// in ";" outside single quotes or to the end, is never broken, so it runs as copied; one wider than
// the line gets a line of its own, wider than width. Prose, which a notice is, names lola in
// passing and has no such command.
func wrapAt(lead, h string, width int, prose bool) string {
	if width == 0 || ansi.StringWidth(lead+h) <= width || width < len(lead)+minHintText {
		return lead + h
	}
	var units []string
	cmd, quoted := "", false
	for word := range strings.SplitSeq(h, " ") { // a command keeps each space; prose needs none twice
		switch {
		case cmd == "" && word == "":
			continue
		case cmd == "" && (prose || word != "lola"):
			units = append(units, word)
			continue
		case cmd == "":
			cmd = word
		default:
			cmd += " " + word
		}
		for i := 0; i < len(word); i++ {
			switch {
			case quoted:
				quoted = word[i] != '\''
			case word[i] == '\\':
				i++
			case word[i] == '\'':
				quoted = true
			}
		}
		if !quoted && strings.HasSuffix(word, ";") {
			units, cmd = append(units, cmd), ""
		}
	}
	if cmd != "" {
		units = append(units, cmd)
	}
	pad := strings.Repeat(" ", len(lead))
	var lines []string
	cur, empty := lead, true
	for _, u := range units {
		switch {
		case empty:
			cur += u
		case ansi.StringWidth(cur)+1+ansi.StringWidth(u) <= width:
			cur += " " + u
		default:
			lines, cur = append(lines, cur), pad+u
		}
		empty = false
	}
	return strings.Join(append(lines, cur), "\n")
}
