package main

import (
	"encoding/binary"
	"testing"
)

func TestEncodeICOIncludesEveryRequestedSize(t *testing.T) {
	sizes := []int{16, 32, 256}
	data, err := encodeICO(drawAppIcon(), sizes)
	if err != nil {
		t.Fatal(err)
	}
	if got := int(binary.LittleEndian.Uint16(data[4:])); got != len(sizes) {
		t.Fatalf("icon count = %d, want %d", got, len(sizes))
	}
	for i, size := range sizes {
		entry := data[6+i*16:]
		length := int(binary.LittleEndian.Uint32(entry[8:]))
		offset := int(binary.LittleEndian.Uint32(entry[12:]))
		if length < 40 || offset+length > len(data) {
			t.Fatalf("invalid icon entry for size %d", size)
		}
		if got := int(binary.LittleEndian.Uint32(data[offset+4:])); got != size {
			t.Fatalf("DIB width = %d, want %d", got, size)
		}
		if got := int(binary.LittleEndian.Uint32(data[offset+8:])); got != size*2 {
			t.Fatalf("DIB height = %d, want %d", got, size*2)
		}
	}
}
