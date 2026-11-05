// Package native recognises native executables (ELF, Mach-O, PE) by their headers, so lola can
// warn about code it would install that runs outside the agent's sandbox. It only reads: nothing
// here executes, loads or trusts a binary, and every parser runs under recover.
package native

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

const (
	// sniffLen covers the 64-byte DOS header that locates a PE signature.
	sniffLen = 64
	// maxParse is the largest file handed to a stdlib parser.
	maxParse = 1 << 30
	// maxFat bounds a fat Mach-O's slice count; a Java class file's version word falls outside
	// 1..30 for every released version, which is how the two CAFEBABE formats are told apart.
	maxFat = 30
)

// Format is the container format of a native binary.
type Format string

const (
	ELF   Format = "ELF"
	MachO Format = "Mach-O"
	PE    Format = "PE"
)

// WithArticle is the format with the indefinite article that reads right before it: "an ELF",
// "a Mach-O", "a PE".
func (f Format) WithArticle() string {
	if f == ELF {
		return "an " + string(f)
	}
	return "a " + string(f)
}

// Binary is one native binary found in a file list.
type Binary struct {
	Path      string   // slash path as passed in
	Format    Format   // the format the header claims, even when Malformed
	Kind      string   // "executable", "shared library", "object file", "bundle", "core file", "unknown", "unparsed (larger than 1 GiB)"
	Arch      []string // one per slice of a fat binary
	OS        string   // "Linux", "FreeBSD", "Unix-like", "Apple platforms", "Windows", ""
	Malformed bool     // the header matched but the stdlib parser rejected the file
	Size      int64
}

// Find reports the native binaries among files, slash paths relative to root, sorted by Path.
// Files that are not native binaries are skipped; a file that cannot be opened is an error.
func Find(root *os.Root, files []string) ([]Binary, error) {
	var out []Binary
	for _, rel := range files {
		f, b, ok, err := open(root, rel)
		if err != nil {
			return nil, err
		}
		if ok {
			parse(f, &b, nil)
		}
		if f != nil {
			_ = f.Close()
		}
		if ok {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b Binary) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// open opens rel through root and sniffs it. The caller closes the returned file whenever it is
// non-nil. ok is false for anything that is not a regular file with a native-binary header.
func open(root *os.Root, rel string) (f *os.File, b Binary, ok bool, err error) {
	name := untrusted.Text(rel, untrusted.MaxName)
	f, err = root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, Binary{}, false, fmt.Errorf("%s: %w", name, untrusted.CleanErr(err))
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, Binary{}, false, fmt.Errorf("%s: %w", name, untrusted.CleanErr(err))
	}
	if !fi.Mode().IsRegular() {
		return f, Binary{}, false, nil
	}
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		_ = f.Close()
		return nil, Binary{}, false, fmt.Errorf("%s: %w", name, untrusted.CleanErr(err))
	}
	format, ok := sniff(f, head[:n], fi.Size())
	if !ok {
		return f, Binary{}, false, nil
	}
	return f, Binary{Path: rel, Format: format, Size: fi.Size()}, true, nil
}

// sniff decides from the first bytes whether the file claims to be a native binary.
func sniff(r io.ReaderAt, head []byte, size int64) (Format, bool) {
	switch {
	case bytes.HasPrefix(head, []byte{0x7f, 'E', 'L', 'F'}):
		return ELF, true
	case bytes.HasPrefix(head, []byte{0xcf, 0xfa, 0xed, 0xfe}), bytes.HasPrefix(head, []byte{0xce, 0xfa, 0xed, 0xfe}),
		bytes.HasPrefix(head, []byte{0xfe, 0xed, 0xfa, 0xce}), bytes.HasPrefix(head, []byte{0xfe, 0xed, 0xfa, 0xcf}):
		return MachO, true
	case len(head) >= 8 && bytes.HasPrefix(head, []byte{0xca, 0xfe, 0xba, 0xbe}):
		return MachO, inRange(binary.BigEndian.Uint32(head[4:8]))
	case len(head) >= 8 && bytes.HasPrefix(head, []byte{0xbe, 0xba, 0xfe, 0xca}):
		return MachO, inRange(binary.LittleEndian.Uint32(head[4:8]))
	case len(head) >= sniffLen && head[0] == 'M' && head[1] == 'Z':
		lfanew := int64(binary.LittleEndian.Uint32(head[0x3c:]))
		sig := make([]byte, 4)
		if lfanew+4 <= size {
			if _, err := r.ReadAt(sig, lfanew); err == nil && bytes.Equal(sig, []byte{'P', 'E', 0, 0}) {
				return PE, true
			}
		}
	}
	return "", false
}

func inRange(nfat uint32) bool { return nfat >= 1 && nfat <= maxFat }

// imports is what Describe needs from the parsed file beyond Binary.
type imports struct {
	libraries []string
	dynamic   bool // an ELF interpreter or at least one imported library
}

// parse fills b from the stdlib parser for b.Format and, when want is non-nil, want too. Any
// failure, including a parser panic, marks b Malformed. A file over maxParse is not parsed.
func parse(f *os.File, b *Binary, want *imports) {
	if b.Size > maxParse {
		b.Kind = "unparsed (larger than 1 GiB)"
		return
	}
	defer func() {
		if r := recover(); r != nil {
			b.Malformed = true
		}
	}()
	var err error
	switch b.Format {
	case ELF:
		err = parseELF(f, b, want)
	case MachO:
		err = parseMachO(f, b, want)
	case PE:
		err = parsePE(f, b, want)
	}
	if err != nil {
		b.Malformed = true
	}
}

func parseELF(f *os.File, b *Binary, want *imports) error {
	ef, err := elf.NewFile(f)
	if err != nil {
		return err
	}
	hasInterp := slices.ContainsFunc(ef.Progs, func(p *elf.Prog) bool { return p.Type == elf.PT_INTERP })
	switch ef.Type {
	case elf.ET_EXEC:
		b.Kind = "executable"
	case elf.ET_DYN:
		b.Kind = "shared library"
		if hasInterp {
			b.Kind = "executable"
		}
	case elf.ET_REL:
		b.Kind = "object file"
	case elf.ET_CORE:
		b.Kind = "core file"
	default:
		b.Kind = "unknown"
	}
	b.Arch = []string{elfArch(ef.Machine)}
	b.OS = elfOS(ef.OSABI)
	if want != nil {
		want.libraries, _ = ef.ImportedLibraries()
		want.dynamic = hasInterp || len(want.libraries) > 0
	}
	return nil
}

func elfArch(m elf.Machine) string {
	switch m {
	case elf.EM_X86_64:
		return "x86-64"
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_386:
		return "x86"
	case elf.EM_ARM:
		return "arm"
	case elf.EM_RISCV:
		return "riscv"
	case elf.EM_PPC64:
		return "ppc64"
	case elf.EM_S390:
		return "s390x"
	}
	return strings.ToLower(strings.TrimPrefix(m.String(), "EM_"))
}

func elfOS(o elf.OSABI) string {
	switch o {
	case elf.ELFOSABI_LINUX:
		return "Linux"
	case elf.ELFOSABI_FREEBSD:
		return "FreeBSD"
	case elf.ELFOSABI_NETBSD:
		return "NetBSD"
	case elf.ELFOSABI_OPENBSD:
		return "OpenBSD"
	case elf.ELFOSABI_SOLARIS:
		return "Solaris"
	case elf.ELFOSABI_NONE:
		return "Unix-like"
	}
	return ""
}

func parseMachO(f *os.File, b *Binary, want *imports) error {
	var parts []*macho.File
	if ff, err := macho.NewFatFile(f); err == nil {
		for _, a := range ff.Arches {
			parts = append(parts, a.File)
		}
	} else if errors.Is(err, macho.ErrNotFat) {
		mf, err := macho.NewFile(f)
		if err != nil {
			return err
		}
		parts = []*macho.File{mf}
	} else {
		return err
	}
	b.Kind = machoKind(parts[0].Type)
	b.OS = "Apple platforms"
	for _, s := range parts {
		b.Arch = append(b.Arch, machoArch(s.Cpu))
	}
	if want != nil {
		want.libraries, _ = parts[0].ImportedLibraries()
		want.dynamic = len(want.libraries) > 0
	}
	return nil
}

func machoKind(t macho.Type) string {
	switch t {
	case macho.TypeExec:
		return "executable"
	case macho.TypeDylib:
		return "shared library"
	case macho.TypeBundle:
		return "bundle"
	case macho.TypeObj:
		return "object file"
	}
	return "unknown"
}

func machoArch(c macho.Cpu) string {
	switch c {
	case macho.CpuAmd64:
		return "x86-64"
	case macho.CpuArm64:
		return "arm64"
	case macho.Cpu386:
		return "x86"
	case macho.CpuArm:
		return "arm"
	case macho.CpuPpc64:
		return "ppc64"
	}
	return strings.ToLower(strings.TrimPrefix(c.String(), "Cpu"))
}

func parsePE(f *os.File, b *Binary, want *imports) error {
	pf, err := pe.NewFile(f)
	if err != nil {
		return err
	}
	b.Kind = "executable"
	if pf.Characteristics&pe.IMAGE_FILE_DLL != 0 {
		b.Kind = "shared library"
	}
	b.OS = "Windows"
	switch pf.Machine {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		b.Arch = []string{"x86-64"}
	case pe.IMAGE_FILE_MACHINE_ARM64:
		b.Arch = []string{"arm64"}
	case pe.IMAGE_FILE_MACHINE_I386:
		b.Arch = []string{"x86"}
	case pe.IMAGE_FILE_MACHINE_ARMNT:
		b.Arch = []string{"arm"}
	default:
		b.Arch = []string{fmt.Sprintf("0x%04x", pf.Machine)}
	}
	if want != nil {
		want.libraries, _ = pf.ImportedLibraries()
		want.dynamic = len(want.libraries) > 0
	}
	return nil
}
