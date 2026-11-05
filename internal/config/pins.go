package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/activeprofile"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// pinsHeader starts a pins.yaml lola writes, when the file has no leading comment of its own.
const pinsHeader = "# Written by lola: the versions installed from your profiles at user scope.\n" +
	"# Safe to sync between machines. Hand edits apply until lola next writes this file.\n"

// pinsHint coaches a pins.yaml that does not read: lola writes it again from what is installed.
const pinsHint = "fix it by hand, or remove it: the next lola sync --user writes it again from what is installed"

// remoteWho names a remote profile in source.CheckRemote's refusals (profile.RemoteWho, which
// this package cannot import: profile imports config).
const remoteWho = "a remote profile"

// Pin is the version installed for one entry of yours at user scope, keyed
// by the entry's Source (as the scope reads it), Ref and Path; Commit (git) and Digest pin it.
type Pin struct{ Source, Ref, Path, Commit, Digest string }

// Remote is the record of a remote profile you have: the source,
// ref, path, commit and digest its source.json holds.
type Remote struct{ Source, Ref, Path, Commit, Digest string }

// Pins is pins.yaml as read, every field checked: Modules by module name, Remotes by remote
// profile name. read is the bytes it was read from (nil: missing).
type Pins struct {
	Path    string
	Modules map[string][]Pin
	Remotes map[string]Remote
	read    []byte
}

// LoadPins reads pins.yaml at path: through yamldoc.Read, which follows a
// symbolic link (a dotfile manager's) and caps the size; every key known; module names by the
// module-name rule, remote names as a qualified remote:<name> is checked (activeprofile.Check),
// each pin and record as untrusted input (Pin.Check, Remote.Check). Each refusal names the file and
// the line. A missing or empty file holds nothing.
func LoadPins(path string) (*Pins, error) {
	p := &Pins{Path: path, Modules: map[string][]Pin{}, Remotes: map[string]Remote{}}
	d, b, err := yamldoc.Read(path, Version)
	if err != nil {
		return nil, err
	}
	p.read = b
	root := yamldoc.Root(d)
	if root == nil {
		return p, nil
	}
	dc := doc{path: path}
	return p, dc.mapping(root, "", map[string]func(*yaml.Node, string) error{
		"version": dc.version,
		"modules": func(n *yaml.Node, at string) error { return p.decodeModules(dc, n, at) },
		"remotes": func(n *yaml.Node, at string) error { return p.decodeRemotes(dc, n, at) },
	}, "version")
}

// record is the five fields of a pin or a record, read from the mapping n at at.
func record(d doc, n *yaml.Node, at string) (Pin, error) {
	var r Pin
	str := func(dst *string) func(*yaml.Node, string) error {
		return func(n *yaml.Node, at string) error {
			v, err := d.scalar(n, at, pinsHint)
			*dst = v
			return err
		}
	}
	err := d.mapping(n, at, map[string]func(*yaml.Node, string) error{"source": str(&r.Source), "ref": str(&r.Ref), "path": str(&r.Path),
		"commit": str(&r.Commit), "digest": str(&r.Digest)})
	return r, err
}

func (p *Pins) decodeModules(d doc, n *yaml.Node, at string) error {
	if isNull(n) {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return d.fail(n, at, "must be a mapping of module names, not "+kindName(n), pinsHint)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		sub := join(at, k.Value)
		if err := module.ValidateName("module", k.Value); err != nil {
			return d.fail(k, sub, err.Error(), pinsHint)
		}
		if v.Kind != yaml.SequenceNode {
			return d.fail(v, sub, "must be a list of pins, not "+kindName(v), pinsHint)
		}
		for j, item := range v.Content {
			at := fmt.Sprintf("%s[%d]", sub, j)
			pin, err := record(d, item, at)
			if err != nil {
				return err
			}
			if err := pin.Check(); err != nil {
				return d.fail(item, at, untrusted.Text(err.Error(), 2*untrusted.MaxName), pinsHint)
			}
			p.Modules[k.Value] = append(p.Modules[k.Value], pin)
		}
	}
	return nil
}

func (p *Pins) decodeRemotes(d doc, n *yaml.Node, at string) error {
	if isNull(n) {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return d.fail(n, at, "must be a mapping of remote profile names, not "+kindName(n), pinsHint)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		sub := join(at, k.Value)
		if err := activeprofile.Check("remote:" + k.Value); err != nil {
			return d.fail(k, sub, untrusted.Text(err.Error(), 2*untrusted.MaxName), pinsHint)
		}
		r, err := record(d, v, sub)
		if err != nil {
			return err
		}
		if err := Remote(r).Check(); err != nil {
			return d.fail(v, sub, untrusted.Text(err.Error(), 2*untrusted.MaxName), pinsHint)
		}
		p.Remotes[k.Value] = Remote(r)
	}
	return nil
}

// Check applies to pin the rules an entry's pin has, on every read and
// before a write: the recorded-source rules (source.Recorded.Check), and, lexically, the engine's
// pin-shape rules (checkPinShape): a source on this machine is never pinned, every pin has its
// digest, a git one its commit, and only git has a ref.
func (pin Pin) Check() error {
	if err := (source.Recorded{Source: pin.Source, Ref: pin.Ref, Path: pin.Path, Commit: pin.Commit, Digest: pin.Digest}).Check(); err != nil {
		return err
	}
	switch {
	case source.IsFolderForm(pin.Source):
		return errors.New("a folder is never pinned")
	case source.Host(pin.Source) == "":
		return errors.New("a source on this machine is never pinned; pins.yaml holds no machine paths")
	case pin.Digest == "":
		return errors.New("digest is missing")
	case source.IsGitForm(pin.Source) && pin.Commit == "":
		return errors.New("commit is missing")
	case !source.IsGitForm(pin.Source) && pin.Ref != "":
		return errors.New("only a git source has a ref")
	}
	return nil
}

// Check applies to r, on every read and before a write, a remote profile's source rules
// (source.CheckRemote: a git repository or an https archive at an allowed host, never this
// machine) and its pin's (Pin.Check).
func (r Remote) Check() error {
	if err := source.CheckRemote(r.Source, remoteWho); err != nil {
		return err
	}
	return Pin(r).Check()
}

// Find is the pin of module whose key is source, ref and path; nil when there is none (an edited
// entry resolves afresh).
func (p *Pins) Find(module, source, ref, path string) *Pin {
	for _, pin := range p.Modules[module] {
		if pin.Source == source && pin.Ref == ref && pin.Path == path {
			return &pin
		}
	}
	return nil
}

// Bytes is the pins.yaml lola writes holding modules and remotes: the comment lines the file
// starts with (lola's header when it starts with none), version, then modules and remotes, each
// by name, a module's pins in the order given, each pin and record a flow mapping. A module with
// no pin, and a section with nothing in it, are left out: LoadPins reads neither back.
func (p *Pins) Bytes(modules map[string][]Pin, remotes map[string]Remote) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(leadingComment(p.read))
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{yamldoc.Str("version"), {Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"}}}
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, name := range slices.Sorted(maps.Keys(modules)) {
		if len(modules[name]) == 0 {
			continue
		}
		list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, pin := range modules[name] {
			list.Content = append(list.Content, flowRecord(pin))
		}
		m.Content = append(m.Content, yamldoc.Str(name), list)
	}
	if len(m.Content) > 0 {
		root.Content = append(root.Content, yamldoc.Str("modules"), m)
	}
	if len(remotes) > 0 {
		r := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, name := range slices.Sorted(maps.Keys(remotes)) {
			r.Content = append(r.Content, yamldoc.Str(name), flowRecord(Pin(remotes[name])))
		}
		root.Content = append(root.Content, yamldoc.Str("remotes"), r)
	}
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := errors.Join(enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}), enc.Close()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// flowRecord is a pin or record as a flow mapping, its empty fields left out.
func flowRecord(r Pin) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	for _, f := range [][2]string{{"source", r.Source}, {"ref", r.Ref}, {"path", r.Path}, {"commit", r.Commit}, {"digest", r.Digest}} {
		if f[1] != "" {
			n.Content = append(n.Content, yamldoc.Str(f[0]), yamldoc.Str(f[1]))
		}
	}
	return n
}

// leadingComment is the comment lines b starts with, the file's own header, as written: blank
// lines among and after them are kept, so a file lola writes again is byte for byte the same.
// lola's header when the file starts with no comment.
func leadingComment(b []byte) string {
	var head strings.Builder
	comment := false
	for _, line := range strings.SplitAfter(string(b), "\n") {
		text := strings.TrimSpace(line)
		if text != "" && !strings.HasPrefix(text, "#") {
			break
		}
		comment = comment || text != ""
		head.WriteString(line)
	}
	if !comment {
		return pinsHeader
	}
	if !strings.HasSuffix(head.String(), "\n") {
		head.WriteString("\n")
	}
	return head.String()
}

// Same reports whether b are the bytes the file held when it was read: a write of them changes
// nothing, and is left out.
func (p *Pins) Same(b []byte) bool { return p.read != nil && bytes.Equal(b, p.read) }

// Current checks, just before lola writes pins.yaml, that the file still holds the bytes it was
// read from (missing, when it was), reading it as LoadPins does.
func (p *Pins) Current() error {
	_, now, err := yamldoc.Read(p.Path, Version)
	if err != nil {
		return err
	}
	if (now == nil) != (p.read == nil) || !bytes.Equal(now, p.read) {
		return errs.WithHints(fmt.Errorf("%s changed while lola was reading it; nothing was written", untrusted.Text(p.Path, untrusted.MaxName)),
			"run the same command again")
	}
	return nil
}
