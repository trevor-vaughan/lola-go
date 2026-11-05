// Package nativetest builds minimal, byte-exact native binaries for specs. Each is the smallest
// header the matching debug/* parser accepts; none is runnable.
package nativetest

import "encoding/binary"

// ELF64 is a little-endian x86-64 ET_EXEC header with no program or section headers.
func ELF64() []byte { return elf64(2, 0, 0) }

// ELF64Dyn is an ET_DYN x86-64 file with a PT_INTERP program header, as a PIE executable has.
func ELF64Dyn() []byte {
	b := elf64(3, 64, 1)
	ph := make([]byte, 56)
	binary.LittleEndian.PutUint32(ph[0:], 3)   // p_type = PT_INTERP
	binary.LittleEndian.PutUint32(ph[4:], 4)   // p_flags
	binary.LittleEndian.PutUint64(ph[8:], 120) // p_offset
	binary.LittleEndian.PutUint64(ph[32:], 1)  // p_filesz
	return append(append(b, ph...), 0)
}

func elf64(typ uint16, phoff uint64, phnum uint16) []byte {
	b := make([]byte, 64)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0})
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], 0x3e) // EM_X86_64
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint64(b[32:], phoff)
	binary.LittleEndian.PutUint16(b[52:], 64) // e_ehsize
	binary.LittleEndian.PutUint16(b[54:], 56) // e_phentsize
	binary.LittleEndian.PutUint16(b[56:], phnum)
	binary.LittleEndian.PutUint16(b[58:], 64) // e_shentsize
	return b
}

// MachO64 is a little-endian arm64 MH_EXECUTE header with no load commands.
func MachO64() []byte { return machO64(0x0100000c) }

func machO64(cpu uint32) []byte {
	b := make([]byte, 32)
	binary.LittleEndian.PutUint32(b[0:], 0xfeedfacf)
	binary.LittleEndian.PutUint32(b[4:], cpu)
	binary.LittleEndian.PutUint32(b[12:], 2) // MH_EXECUTE
	return b
}

// MachOFat is a universal binary holding one x86-64 slice.
func MachOFat() []byte {
	b := make([]byte, 28)
	binary.BigEndian.PutUint32(b[0:], 0xcafebabe)
	binary.BigEndian.PutUint32(b[4:], 1) // nfat_arch
	binary.BigEndian.PutUint32(b[8:], 0x01000007)
	binary.BigEndian.PutUint32(b[12:], 3)
	binary.BigEndian.PutUint32(b[16:], 32) // offset
	binary.BigEndian.PutUint32(b[20:], 32) // size
	b = append(b, make([]byte, 4)...)      // pad to offset 32
	return append(b, machO64(0x01000007)...)
}

// PE64 is a DOS header, the PE signature and a COFF header for an x86-64 executable image,
// padded to 96 bytes because debug/pe reads a 96-byte DOS header.
func PE64() []byte {
	b := make([]byte, 96)
	b[0], b[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(b[0x3c:], 0x40)
	copy(b[64:], []byte{'P', 'E', 0, 0})
	binary.LittleEndian.PutUint16(b[68:], 0x8664)
	binary.LittleEndian.PutUint16(b[86:], 0x0002)
	return b
}

// JavaClass starts with the same CAFEBABE magic as a fat Mach-O; its next word is a Java version.
func JavaClass() []byte {
	return append([]byte{0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 0x34}, 1, 2, 3, 4, 5, 6, 7, 8)
}
