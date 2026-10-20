// Package xerox implements the port 9400 protocol observed in the supplied
// WorkCentre 3225 capture. No vendor drivers or SANE runtime are required.
package xerox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"log"
	"net"
	"time"
)

var ErrNoPaper = errors.New("document feeder is empty")
var ErrBusy = errors.New("scanner is busy")
var ErrPaperJam = errors.New("document feeder jammed")
var ErrCoverOpen = errors.New("scanner cover is open")

const (
	cmdAbort     byte = 0x06
	cmdInquiry   byte = 0x12
	cmdReserve   byte = 0x16
	cmdRelease   byte = 0x17
	cmdSetWindow byte = 0x24
	cmdRead      byte = 0x28
	cmdReadImage byte = 0x29
	cmdAdvance   byte = 0x31
)

var dpiCodes = map[int]byte{75: 0, 100: 10, 150: 2, 200: 9, 300: 5, 600: 7}

type Info struct {
	Model        string `json:"model"`
	Width        int    `json:"width_1200dpi"`
	PlatenHeight int    `json:"platen_height_1200dpi"`
	ADFHeight    int    `json:"adf_height_1200dpi"`
	Resolutions  []int  `json:"resolutions"`
	PaperLoaded  bool   `json:"paper_loaded"`
}

func ParseInquiry(b []byte) (Info, error) {
	if len(b) != 70 || b[0] != 0xa8 || b[1] != 0 || b[3] != 0x10 {
		return Info{}, fmt.Errorf("invalid scanner identity response (%x)", b)
	}
	i := Info{Model: string(bytes.TrimSpace(b[4:36])), Width: int(binary.BigEndian.Uint32(b[40:44])),
		PlatenHeight: int(binary.BigEndian.Uint32(b[60:64])), ADFHeight: int(binary.BigEndian.Uint32(b[56:60])),
		PaperLoaded: b[53] == 2 && b[38]&3 != 0}
	mask := uint32(b[55])<<16 | uint32(b[36])<<8 | uint32(b[37])
	for _, entry := range []struct{ dpi, bit int }{{75, 0}, {100, 12}, {150, 1}, {200, 4}, {300, 5}, {600, 8}} {
		if mask&(1<<entry.bit) != 0 {
			i.Resolutions = append(i.Resolutions, entry.dpi)
		}
	}
	if !bytes.Contains(b[4:36], []byte("WorkCentre 3225")) {
		return Info{}, fmt.Errorf("unsupported scanner %q; this bridge is validated for WorkCentre 3225", i.Model)
	}
	if b[50]&0x40 == 0 || i.Width <= 0 || i.Width > 14400 || i.PlatenHeight <= 0 || i.PlatenHeight > 20000 || i.ADFHeight < i.PlatenHeight || i.ADFHeight > 24000 || len(i.Resolutions) == 0 {
		return Info{}, errors.New("scanner reported unsupported JPEG capabilities or geometry")
	}
	return i, nil
}

// Settings uses eSCL's 1/300 inch coordinate system.
type Settings struct {
	DPI                 int
	X, Y, Width, Height int
	Source              string
	Gray                bool
	Format              string
}

func (s Settings) Validate(i Info) error {
	if s.Source != "Platen" && s.Source != "Feeder" {
		return errors.New("InputSource must be Platen or Feeder")
	}
	maxH := i.PlatenHeight / 4
	if s.Source == "Feeder" {
		maxH = i.ADFHeight / 4
	}
	if s.X < 0 || s.Y < 0 || s.Width < 4 || s.Height < 4 || s.Width > i.Width/4 || s.Height > maxH || s.X > i.Width/4-s.Width || s.Y > maxH-s.Height {
		return errors.New("scan region is outside the selected source")
	}
	ok := false
	for _, dpi := range i.Resolutions {
		if s.DPI == dpi {
			ok = true
		}
	}
	if !ok {
		return errors.New("unsupported resolution")
	}
	if s.Format != "image/jpeg" && s.Format != "image/png" {
		return errors.New("DocumentFormat must be image/jpeg or image/png")
	}
	return nil
}

// Window always scans from the origin and crops in software. Hardware offsets
// have only 1/100 inch precision, too coarse for Image Capture's crop rectangles.
func Window(s Settings) []byte {
	b := make([]byte, 25)
	copy(b, []byte{0x1b, 0xa8, cmdSetWindow, 0x13, 0x30})
	binary.BigEndian.PutUint32(b[5:9], uint32((s.X+s.Width)*4))
	binary.BigEndian.PutUint32(b[9:13], uint32((s.Y+s.Height)*4))
	b[13], b[14] = dpiCodes[s.DPI], dpiCodes[s.DPI]
	b[19], b[20], b[21], b[22], b[23] = 5, 6, 1, 2, 0x40
	if s.Source == "Feeder" {
		b[23] = 0x20
	}
	return b
}

type Client struct {
	conn         net.Conn
	ctx          context.Context
	stop         func() bool
	reserved     bool
	pageComplete bool
	Debug        bool
	settings     Settings
	timeout      time.Duration
}

func Dial(ctx context.Context, address string, timeout time.Duration) (*Client, error) {
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect to scanner %s: %w", address, err)
	}
	c := &Client{conn: conn, ctx: ctx, timeout: timeout}
	c.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	return c, nil
}

func (c *Client) command(cmd byte, packet []byte) ([]byte, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	if packet == nil {
		packet = []byte{0x1b, 0xa8, cmd, 0}
	}
	if _, err := io.Copy(c.conn, bytes.NewReader(packet)); err != nil {
		return nil, err
	}
	if cmd == cmdReadImage {
		return nil, nil
	}
	n := 32
	if cmd == cmdInquiry {
		n = 70
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c.conn, b); err != nil {
		return nil, fmt.Errorf("command 0x%02x: %w", cmd, err)
	}
	if c.Debug {
		log.Printf("Xerox command %02x reply %x", cmd, b)
	}
	if b[0] != 0xa8 || int(b[2])+3 != len(b) {
		return nil, errors.New("invalid scanner response framing")
	}
	switch b[1] {
	case 0:
		return b, nil
	case 8:
		return b, ErrBusy
	case 4:
		return b, context.Canceled
	case 2:
		off := 4
		if cmd == cmdRead {
			off = 12
		}
		state := binary.BigEndian.Uint16(b[off : off+2])
		switch {
		case state&0x20 != 0:
			return b, ErrPaperJam
		case state&0x40 != 0:
			return b, ErrCoverOpen
		case state&0x10 != 0:
			return b, ErrNoPaper
		case state&0x480 != 0:
			return b, ErrBusy
		case state == 1:
			return b, nil
		default:
			return b, fmt.Errorf("scanner state 0x%04x", state)
		}
	default:
		return b, fmt.Errorf("unknown scanner status 0x%02x", b[1])
	}
}

func (c *Client) wait(cmd byte) ([]byte, error) {
	for {
		b, err := c.command(cmd, nil)
		if !errors.Is(err, ErrBusy) {
			return b, err
		}
		select {
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (c *Client) Inquiry() (Info, error) {
	b, err := c.command(cmdInquiry, nil)
	if err != nil {
		return Info{}, err
	}
	return ParseInquiry(b)
}

func (c *Client) Close() error {
	defer c.conn.Close()
	if c.stop != nil {
		c.stop()
	}
	if c.reserved && c.ctx.Err() == nil {
		// ABORT can discard a prefetched ADF sheet. A successful page needs
		// only RELEASE; abort is reserved for incomplete acquisitions.
		if !c.pageComplete {
			_, _ = c.command(cmdAbort, nil)
		}
		_, _ = c.command(cmdRelease, nil)
		c.reserved = false
	}
	return nil
}

type Band struct {
	Data          []byte
	Width, Height int
	Final         bool
}

// DecodeBand handles transport padding after EOI and the leading zero padding
// reported with the 3225 flatbed by SANE users. Never feed padding to libjpeg.
func DecodeBand(b Band) (image.Image, error) {
	start := bytes.Index(b.Data, []byte{0xff, 0xd8})
	if start < 0 || start > 512 {
		return nil, errors.New("JPEG strip has no SOI marker")
	}
	for _, v := range b.Data[:start] {
		if v != 0 {
			return nil, errors.New("unexpected bytes before JPEG strip")
		}
	}
	data := b.Data[start:]
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("JPEG strip header: %w", err)
	}
	if config.Width != b.Width || config.Height < b.Height || config.Height > b.Height+16 || b.Width <= 0 || b.Width > 7200 || b.Height <= 0 || b.Height > 4096 {
		return nil, fmt.Errorf("invalid strip dimensions JPEG=%dx%d protocol=%dx%d", config.Width, config.Height, b.Width, b.Height)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("JPEG strip: %w", err)
	}
	return img, nil
}

type Assembler struct {
	Image draw.Image
	crop  image.Rectangle
	row   int
}

func NewAssembler(s Settings) *Assembler {
	// Use differences of absolute coordinates so fractional pixel crops don't drift.
	r := image.Rect(s.X*s.DPI/300, s.Y*s.DPI/300, (s.X+s.Width)*s.DPI/300, (s.Y+s.Height)*s.DPI/300)
	var dst draw.Image = image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	if s.Gray {
		dst = image.NewGray(image.Rect(0, 0, r.Dx(), r.Dy()))
	}
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	return &Assembler{Image: dst, crop: r}
}

func (a *Assembler) Add(b Band) error {
	if b.Width < a.crop.Max.X {
		return fmt.Errorf("incomplete scan width: got %d pixels, need %d", b.Width, a.crop.Max.X)
	}
	img, err := DecodeBand(b)
	if err != nil {
		return err
	}
	bandRect := image.Rect(0, a.row, b.Width, a.row+b.Height)
	part := bandRect.Intersect(a.crop)
	if !part.Empty() {
		dst := part.Sub(a.crop.Min)
		draw.Draw(a.Image, dst, img, image.Pt(part.Min.X, part.Min.Y-a.row), draw.Src)
	}
	a.row += b.Height
	return nil
}

func (a *Assembler) Finish(source string) (image.Image, error) {
	if a.row < a.crop.Max.Y {
		if source != "Feeder" || a.row <= a.crop.Min.Y {
			return nil, fmt.Errorf("incomplete scan: got %d rows, need %d", a.row, a.crop.Max.Y)
		}
		// ADF height is a limit. The paper sensor can end an A4/Letter sheet
		// before the requested Legal-sized window; preserve its actual length.
		r := image.Rect(0, 0, a.crop.Dx(), a.row-a.crop.Min.Y)
		return a.Image.(interface {
			SubImage(image.Rectangle) image.Image
		}).SubImage(r), nil
	}
	return a.Image, nil
}

func (c *Client) Scan(s Settings) (image.Image, error) {
	continuing := c.reserved && c.pageComplete && s.Source == "Feeder"
	if continuing && s != c.settings {
		return nil, errors.New("cannot change feeder settings during a batch")
	}
	c.pageComplete = false
	if !continuing {
		i, err := c.Inquiry()
		if err != nil {
			return nil, err
		}
		if err = s.Validate(i); err != nil {
			return nil, err
		}
		if _, err = c.wait(cmdReserve); err != nil {
			return nil, err
		}
		c.reserved = true
		if _, err = c.command(cmdSetWindow, Window(s)); err != nil {
			return nil, err
		}
		if _, err = c.wait(cmdAdvance); err != nil {
			return nil, err
		}
		c.settings = s
	} else {
		// The vendor's two-page capture advances the ADF with OBJECT_POSITION
		// for each page, without releasing or repeating SET_WINDOW.
		if _, err := c.wait(cmdAdvance); err != nil {
			return nil, err
		}
	}
	a := NewAssembler(s)
	for n := 0; n < 10000; n++ {
		r, err := c.wait(cmdRead)
		if err != nil {
			if errors.Is(err, ErrNoPaper) && a.row > 0 {
				return nil, fmt.Errorf("feeder ran out of paper before the final image strip (%d rows received)", a.row)
			}
			return nil, err
		}
		if r[3] != 0x80 && r[3] != 0x81 {
			return nil, fmt.Errorf("unexpected image message 0x%02x", r[3])
		}
		length := int(binary.BigEndian.Uint32(r[4:8]))
		if length <= 0 || length > 32<<20 {
			return nil, fmt.Errorf("invalid image strip length %d", length)
		}
		band := Band{Width: int(binary.BigEndian.Uint16(r[10:12])), Height: int(binary.BigEndian.Uint16(r[8:10])), Final: r[3] == 0x81}
		if a.row+band.Height > (s.Y+s.Height)*s.DPI/300+64 {
			return nil, errors.New("scanner sent excess image rows")
		}
		if _, err = c.command(cmdReadImage, nil); err != nil {
			return nil, err
		}
		band.Data = make([]byte, length)
		if _, err = io.ReadFull(c.conn, band.Data); err != nil {
			return nil, fmt.Errorf("read image strip: %w", err)
		}
		if err = a.Add(band); err != nil {
			return nil, err
		}
		if band.Final {
			img, err := a.Finish(s.Source)
			c.pageComplete = err == nil
			return img, err
		}
	}
	return nil, errors.New("scanner exceeded maximum number of strips")
}
