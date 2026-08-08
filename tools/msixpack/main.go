// msixpack builds an unsigned MSIX (OPC zip) for Microsoft Store submission
// from a staged directory: owldrop.exe + AppxManifest.xml + Assets/*.png.
//
// It emits the container layout makeappx produces, per the APPX format:
//
//   - each 64 KiB block of every file is DEFLATE-compressed as an
//     INDEPENDENT raw stream and concatenated into the zip entry
//   - the block map carries per-block Hash (base64 SHA-256 of the
//     UNCOMPRESSED block) and Size (compressed byte count)
//   - local file headers carry real CRC32/sizes — NO data descriptors
//   - file data is 4-byte aligned via LFH extra-field padding; LfhSize in
//     the block map includes that padding
//
// Partner Center re-signs the package; no signature is needed here.
package main

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type entry struct {
	name  string
	raw   []byte
	comp  []byte // per-block deflate concatenation
	sizes []uint32
}

func deflateBlocks(raw []byte) (comp []byte, sizes []uint32) {
	var out bytes.Buffer
	for i := 0; i < len(raw); i += 65536 {
		end := i + 65536
		if end > len(raw) {
			end = len(raw)
		}
		var b bytes.Buffer
		w, err := flate.NewWriter(&b, flate.DefaultCompression)
		if err != nil {
			panic(err)
		}
		if _, err := w.Write(raw[i:end]); err != nil {
			panic(err)
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
		out.Write(b.Bytes())
		sizes = append(sizes, uint32(b.Len()))
	}
	return out.Bytes(), sizes
}

// alignPad computes the LFH extra-field padding so file data starts on a
// 4-byte boundary, using a representable extra length (0, 4, 6, 8, …).
func alignPad(dataStartOff int) int {
	pad := (4 - dataStartOff%4) % 4
	if pad == 1 || pad == 2 || pad == 3 {
		pad += 4
	}
	return pad
}

func main() {
	var stage, out string
	args := os.Args[1:]
	for i := 0; i < len(args); i += 2 {
		switch args[i] {
		case "-stage":
			stage = args[i+1]
		case "-out":
			out = args[i+1]
		}
	}
	if stage == "" || out == "" {
		fmt.Fprintln(os.Stderr, "usage: msixpack -stage DIR -out FILE")
		os.Exit(2)
	}

	manifest, err := os.ReadFile(filepath.Join(stage, "AppxManifest.xml"))
	if err != nil {
		panic(err)
	}
	if strings.Contains(string(manifest), "__") {
		fmt.Fprintln(os.Stderr, "error: unsubstituted placeholder remains in AppxManifest.xml")
		os.Exit(1)
	}
	if err := xml.Unmarshal(manifest, new(any)); err != nil {
		fmt.Fprintln(os.Stderr, "manifest XML invalid:", err)
		os.Exit(1)
	}

	entries := []entry{{name: "AppxManifest.xml", raw: manifest}}
	exe, err := os.ReadFile(filepath.Join(stage, "owldrop.exe"))
	if err != nil {
		panic(err)
	}
	entries = append(entries, entry{name: "owldrop.exe", raw: exe})
	assets, err := filepath.Glob(filepath.Join(stage, "Assets", "*.png"))
	if err != nil {
		panic(err)
	}
	sort.Strings(assets)
	for _, a := range assets {
		d, err := os.ReadFile(a)
		if err != nil {
			panic(err)
		}
		entries = append(entries, entry{name: "Assets/" + filepath.Base(a), raw: d})
	}
	// Block map is always the last entry.
	bmEntry := entry{name: "AppxBlockMap.xml"}

	// First pass over payload entries: compress + lay out offsets + LFH sizes.
	type layout struct {
		lfhSize uint32
		lfhOff  uint32
		dataOff uint32
	}
	lays := make([]layout, len(entries))
	pos := uint32(0)
	for i := range entries {
		e := &entries[i]
		e.comp, e.sizes = deflateBlocks(e.raw)
		pad := uint32(alignPad(int(pos) + 30 + len(e.name)))
		lfh := uint32(30 + len(e.name) + int(pad))
		lays[i] = layout{lfhSize: lfh, lfhOff: pos, dataOff: pos + lfh}
		pos += lfh + uint32(len(e.comp))
	}

	// Block map content (payload LfhSizes are final; the block map's own LFH
	// is not referenced by it).
	var bm bytes.Buffer
	bm.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	bm.WriteString(`<BlockMap xmlns="http://schemas.microsoft.com/appx/2010/blockmap" HashMethod="http://www.w3.org/2001/04/xmlenc#sha256">` + "\n")
	for i := range entries {
		e := &entries[i]
		fmt.Fprintf(&bm, "  <File Name=\"%s\" Size=\"%d\" LfhSize=\"%d\">\n", e.name, len(e.raw), lays[i].lfhSize)
		for j := 0; j < len(e.raw); j += 65536 {
			end := j + 65536
			if end > len(e.raw) {
				end = len(e.raw)
			}
			sum := sha256.Sum256(e.raw[j:end])
			fmt.Fprintf(&bm, "    <Block Hash=\"%s\" Size=\"%d\"/>\n",
				base64.StdEncoding.EncodeToString(sum[:]), e.sizes[j/65536])
		}
		bm.WriteString("  </File>\n")
	}
	bm.WriteString("</BlockMap>\n")
	bmEntry.raw = bm.Bytes()
	bmEntry.comp, bmEntry.sizes = deflateBlocks(bmEntry.raw)

	// Block map's own layout (block map LFH size is not recorded anywhere).
	pad := uint32(alignPad(int(pos) + 30 + len(bmEntry.name)))
	bmLfh := uint32(30 + len(bmEntry.name) + int(pad))
	bmLfhOff := pos

	// Write the zip.
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	var off uint32
	u16 := func(v uint16) {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], v)
		f.Write(b[:])
		off += 2
	}
	u32 := func(v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		f.Write(b[:])
		off += 4
	}
	bytesWrite := func(b []byte) {
		f.Write(b)
		off += uint32(len(b))
	}
	writeLFH := func(name string, lfhOff uint32, raw, comp []byte, padExtra uint32) {
		u32(0x04034b50)
		u16(20) // version needed
		u16(0)  // flags: no data descriptors
		u16(8)  // method: deflate
		u16(0)  // mod time
		u16(0x21)
		u32(crc32.ChecksumIEEE(raw))
		u32(uint32(len(comp)))
		u32(uint32(len(raw)))
		u16(uint16(len(name)))
		u16(uint16(padExtra))
		bytesWrite([]byte(name))
		if padExtra > 0 {
			// one alignment extra field: id 0xA220, zeroed payload
			x := make([]byte, padExtra)
			binary.LittleEndian.PutUint16(x[0:2], 0xA220)
			binary.LittleEndian.PutUint16(x[2:4], uint16(padExtra-4))
			bytesWrite(x)
		}
		bytesWrite(comp)
	}

	for i := range entries {
		writeLFH(entries[i].name, lays[i].lfhOff, entries[i].raw, entries[i].comp, lays[i].lfhSize-uint32(30+len(entries[i].name)))
	}
	writeLFH(bmEntry.name, bmLfhOff, bmEntry.raw, bmEntry.comp, bmLfh-uint32(30+len(bmEntry.name)))

	cdStart := off
	writeCD := func(name string, lfhOff uint32, raw, comp []byte) {
		u32(0x02014b50)
		u16(20) // version made by
		u16(20) // version needed
		u16(0)
		u16(8) // deflate
		u16(0)
		u16(0x21)
		u32(crc32.ChecksumIEEE(raw))
		u32(uint32(len(comp)))
		u32(uint32(len(raw)))
		u16(uint16(len(name)))
		u16(0) // extra
		u16(0) // comment
		u16(0) // disk
		u16(0) // internal attrs
		u32(0) // external attrs
		u32(lfhOff)
		bytesWrite([]byte(name))
	}
	for i := range entries {
		writeCD(entries[i].name, lays[i].lfhOff, entries[i].raw, entries[i].comp)
	}
	writeCD(bmEntry.name, bmLfhOff, bmEntry.raw, bmEntry.comp)
	cdSize := off - cdStart
	total := uint16(len(entries) + 1)

	u32(0x06054b50)
	u16(0)
	u16(0)
	u16(total)
	u16(total)
	u32(cdSize)
	u32(cdStart)
	u16(0)

	fmt.Printf("wrote %s (%d entries, %d bytes)\n", out, len(entries)+1, off)
}
