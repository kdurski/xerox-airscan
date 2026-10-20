package xerox

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// Public tests generate their own documents and TCP traffic. No scanned pages,
// serial numbers, device MACs, or private network addresses are embedded here.
func fixture(t *testing.T) []Exchange {
	t.Helper()
	info := make([]byte, 70)
	info[0], info[2], info[3] = 0xa8, 67, 0x10
	copy(info[4:36], fmt.Sprintf("%-8s%-24s", "Xerox", "WorkCentre 3225"))
	info[36], info[37], info[38], info[39] = 0x11, 0x33, 0x86, 0x2b
	binary.BigEndian.PutUint32(info[40:44], 10208)
	binary.BigEndian.PutUint32(info[44:48], 16800)
	info[49], info[50], info[53] = 1, 0x40, 1
	binary.BigEndian.PutUint32(info[56:60], 16800)
	binary.BigEndian.PutUint32(info[60:64], 14040)
	good := func() []byte { b := make([]byte, 32); b[0], b[2] = 0xa8, 29; return b }
	command := func(cmd byte) []byte { return []byte{0x1b, 0xa8, cmd, 0} }
	window := []byte{0x1b, 0xa8, 0x24, 0x13, 0x30, 0, 0, 0x27, 0xe0, 0, 0, 0x41, 0xa0, 0, 0, 0, 0, 0, 0, 5, 6, 1, 2, 0x40, 0}
	exchanges := []Exchange{{command(0x12), info}, {command(0x16), good()}, {window, good()}, {command(0x31), good()}}
	for band := 0; band < 28; band++ {
		if band == 0 {
			for n := 0; n < 16; n++ {
				busy := good()
				busy[1] = 8
				exchanges = append(exchanges, Exchange{command(0x28), busy})
			}
		}
		height := 32
		if band == 27 {
			height = 16
		}
		img := image.NewRGBA(image.Rect(0, 0, 638, height))
		for y := 0; y < height; y++ {
			for x := 0; x < 638; x++ {
				shade := uint8((x/8 + band*3 + y) % 256)
				img.SetRGBA(x, y, color.RGBA{R: shade, G: 255 - shade, B: 128, A: 255})
			}
		}
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 85}); err != nil {
			t.Fatal(err)
		}
		data := append(encoded.Bytes(), make([]byte, 16)...)
		status := good()
		status[3] = 0x80
		if band == 27 {
			status[3] = 0x81
		}
		binary.BigEndian.PutUint32(status[4:8], uint32(len(data)))
		binary.BigEndian.PutUint16(status[8:10], uint16(height))
		binary.BigEndian.PutUint16(status[10:12], 638)
		exchanges = append(exchanges, Exchange{command(0x28), status}, Exchange{command(0x29), data})
	}
	empty := good()
	empty[1], empty[5] = 2, 0x10
	exchanges = append(exchanges, Exchange{command(0x31), empty}, Exchange{command(0x06), good()}, Exchange{command(0x17), good()})
	path := filepath.Join(t.TempDir(), "synthetic.pcap")
	if err := os.WriteFile(path, syntheticPCAP(exchanges), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadCapture(path)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func syntheticPCAP(exchanges []Exchange) []byte {
	var capture bytes.Buffer
	for _, v := range []any{uint32(0xa1b2c3d4), uint16(2), uint16(4), uint32(0), uint32(0), uint32(65535), uint32(1)} {
		_ = binary.Write(&capture, binary.LittleEndian, v)
	}
	sequence := []uint32{1000, 2000}
	for _, e := range exchanges {
		for direction, payload := range [][]byte{e.Command, e.Response} {
			for len(payload) > 0 {
				n := min(len(payload), 701)
				part := payload[:n]
				payload = payload[n:]
				frame := make([]byte, 14+20+20+n)
				frame[12], frame[13] = 8, 0
				ip := frame[14:]
				ip[0], ip[8], ip[9] = 0x45, 64, 6
				binary.BigEndian.PutUint16(ip[2:4], uint16(40+n))
				copy(ip[12:16], []byte{192, 0, 2, 1})
				copy(ip[16:20], []byte{192, 0, 2, 10})
				tcp := ip[20:]
				src, dst := uint16(40000), uint16(9400)
				if direction == 1 {
					copy(ip[12:16], []byte{192, 0, 2, 10})
					copy(ip[16:20], []byte{192, 0, 2, 1})
					src, dst = dst, src
				}
				binary.BigEndian.PutUint16(tcp[0:2], src)
				binary.BigEndian.PutUint16(tcp[2:4], dst)
				binary.BigEndian.PutUint32(tcp[4:8], sequence[direction])
				sequence[direction] += uint32(n)
				tcp[12], tcp[13] = 0x50, 0x18
				copy(tcp[20:], part)
				for _, v := range []uint32{1, 0, uint32(len(frame)), uint32(len(frame))} {
					_ = binary.Write(&capture, binary.LittleEndian, v)
				}
				capture.Write(frame)
			}
		}
	}
	return capture.Bytes()
}

func privateCapture(t *testing.T, name string) []Exchange {
	t.Helper()
	dir := os.Getenv("XEROX_TEST_CAPTURE_DIR")
	if dir == "" {
		t.Skip("optional hardware regression: set XEROX_TEST_CAPTURE_DIR to a private capture directory")
	}
	exchanges, err := ReadCapture(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return exchanges
}
