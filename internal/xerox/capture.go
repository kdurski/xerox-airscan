package xerox

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"sort"
)

type Exchange struct{ Command, Response []byte }
type segment struct {
	seq  uint32
	data []byte
}

func joinSegments(parts []segment) ([]byte, error) {
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].seq < parts[j].seq })
	if len(parts) == 0 {
		return nil, errors.New("no scanner TCP payloads")
	}
	var result []byte
	next := parts[0].seq
	for _, p := range parts {
		if p.seq > next {
			return nil, fmt.Errorf("TCP capture has a gap of %d bytes", p.seq-next)
		}
		skip := int(next - p.seq)
		if skip > len(p.data) {
			continue
		}
		if skip > 0 && !bytes.Equal(result[len(result)-skip:], p.data[:skip]) {
			return nil, errors.New("conflicting TCP retransmission")
		}
		result = append(result, p.data[skip:]...)
		next += uint32(len(p.data) - skip)
	}
	return result, nil
}

// ReadCapture reassembles Ethernet/IPv4/TCP from a classic pcap. It rejects
// missing data and multiple connections rather than decoding a partial image.
func ReadCapture(path string) ([]Exchange, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 32<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 32<<20 {
		return nil, errors.New("capture exceeds 32 MiB")
	}
	if len(b) < 24 {
		return nil, errors.New("truncated pcap header")
	}
	var order binary.ByteOrder
	switch string(b[:4]) {
	case "\xd4\xc3\xb2\xa1", "\x4d\x3c\xb2\xa1":
		order = binary.LittleEndian
	case "\xa1\xb2\xc3\xd4", "\xa1\xb2\x3c\x4d":
		order = binary.BigEndian
	default:
		return nil, errors.New("expected classic pcap, not pcapng")
	}
	if order.Uint32(b[20:24]) != 1 {
		return nil, errors.New("expected Ethernet capture")
	}
	var outgoing, incoming []segment
	var connection string
	for p := 24; p < len(b); {
		if p+16 > len(b) {
			return nil, errors.New("truncated pcap record")
		}
		n := int(order.Uint32(b[p+8 : p+12]))
		p += 16
		if n > len(b)-p {
			return nil, errors.New("truncated captured packet")
		}
		frame := b[p : p+n]
		p += n
		if len(frame) < 34 || binary.BigEndian.Uint16(frame[12:14]) != 0x800 {
			continue
		}
		ip := frame[14:]
		ihl := int(ip[0]&15) * 4
		total := int(binary.BigEndian.Uint16(ip[2:4]))
		if ip[9] != 6 {
			continue
		}
		if ihl < 20 || total > len(ip) || total < ihl+20 {
			return nil, errors.New("truncated IP/TCP packet")
		}
		if binary.BigEndian.Uint16(ip[6:8])&0x3fff != 0 {
			return nil, errors.New("fragmented IP is not supported")
		}
		tcp := ip[ihl:total]
		src, dst := binary.BigEndian.Uint16(tcp[:2]), binary.BigEndian.Uint16(tcp[2:4])
		if src != 9400 && dst != 9400 {
			continue
		}
		thl := int(tcp[12]>>4) * 4
		if thl < 20 || thl > len(tcp) {
			return nil, errors.New("invalid TCP header")
		}
		data := tcp[thl:]
		if len(data) == 0 {
			continue
		}
		key := fmt.Sprintf("%x:%d>%x:%d", ip[12:16], src, ip[16:20], dst)
		if src == 9400 {
			key = fmt.Sprintf("%x:%d>%x:%d", ip[16:20], dst, ip[12:16], src)
		}
		if connection != "" && key != connection {
			return nil, errors.New("capture contains multiple scanner connections")
		}
		connection = key
		part := segment{seq: binary.BigEndian.Uint32(tcp[4:8]), data: append([]byte(nil), data...)}
		if dst == 9400 {
			outgoing = append(outgoing, part)
		} else {
			incoming = append(incoming, part)
		}
	}
	requests, err := joinSegments(outgoing)
	if err != nil {
		return nil, err
	}
	responses, err := joinSegments(incoming)
	if err != nil {
		return nil, err
	}
	var exchanges []Exchange
	imageSize := 0
	for len(requests) > 0 {
		if len(requests) < 4 || requests[0] != 0x1b || requests[1] != 0xa8 {
			return nil, errors.New("invalid command stream")
		}
		n := int(requests[3]) + 4
		if requests[2] == 0x24 {
			n = 25
		}
		if n > len(requests) {
			return nil, errors.New("truncated command stream")
		}
		cmd := append([]byte(nil), requests[:n]...)
		requests = requests[n:]
		rn := 32
		if cmd[2] == 0x12 {
			rn = 70
		}
		if cmd[2] == 0x29 {
			rn = imageSize
		}
		if rn <= 0 || rn > len(responses) {
			return nil, errors.New("truncated response stream")
		}
		response := append([]byte(nil), responses[:rn]...)
		responses = responses[rn:]
		if cmd[2] == 0x28 && response[1] == 0 {
			imageSize = int(binary.BigEndian.Uint32(response[4:8]))
		}
		exchanges = append(exchanges, Exchange{Command: cmd, Response: response})
	}
	if len(responses) != 0 {
		return nil, errors.New("unmatched response bytes")
	}
	return exchanges, nil
}

func CaptureImage(exchanges []Exchange) (image.Image, Info, int, error) {
	var info Info
	var bands []Band
	var pending Band
	for _, e := range exchanges {
		switch e.Command[2] {
		case 0x12:
			var err error
			info, err = ParseInquiry(e.Response)
			if err != nil {
				return nil, info, 0, err
			}
		case 0x28:
			if e.Response[1] == 0 {
				pending = Band{Width: int(binary.BigEndian.Uint16(e.Response[10:12])), Height: int(binary.BigEndian.Uint16(e.Response[8:10])), Final: e.Response[3] == 0x81}
			}
		case 0x29:
			pending.Data = e.Response
			bands = append(bands, pending)
		}
	}
	if len(bands) == 0 || !bands[len(bands)-1].Final {
		return nil, info, 0, errors.New("capture has no complete scan")
	}
	h := 0
	for _, band := range bands {
		h += band.Height
	}
	a := NewAssembler(Settings{DPI: 300, Width: bands[0].Width, Height: h})
	for _, band := range bands {
		if err := a.Add(band); err != nil {
			return nil, info, 0, err
		}
	}
	return a.Image, info, len(bands), nil
}
