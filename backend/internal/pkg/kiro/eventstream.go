// Copyright (c) 2026 hank9999. Licensed under MIT.
// Copyright (c) 2026 Harllan He. Licensed under MIT.
// Protocol adaptation provenance is recorded in provenance.go.

package kiro

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"unicode/utf8"
)

var (
	ErrFrame  = errors.New("kiro: invalid eventstream frame")
	ErrLimit  = errors.New("kiro: eventstream limit exceeded")
	ErrHeader = errors.New("kiro: invalid eventstream headers")
	ErrRead   = errors.New("kiro: stream read failed")
)

// Limits bound each allocation. MaxBufferBytes includes the prelude and body;
// no read-ahead or unbounded accumulation is performed. Zero selects defaults.
type Limits struct{ MaxFrameBytes, MaxBufferBytes, MaxHeadersBytes int }

func (l Limits) normalized() (Limits, error) {
	if l.MaxFrameBytes == 0 {
		l.MaxFrameBytes = 16 * 1024 * 1024
	}
	if l.MaxBufferBytes == 0 {
		l.MaxBufferBytes = 16 * 1024 * 1024
	}
	if l.MaxHeadersBytes == 0 {
		l.MaxHeadersBytes = 128 * 1024
	}
	if l.MaxFrameBytes < 16 || l.MaxBufferBytes < 16 || l.MaxHeadersBytes < 0 {
		return l, ErrLimit
	}
	return l, nil
}

// Header retains the wire type. Value is bool, int8/int16/int32/int64,
// []byte, string, int64 milliseconds (timestamp), or [16]byte (UUID).
type Header struct {
	Name  string
	Type  byte
	Value any
}

// Frame owns its storage; accessors return independent copies of mutable data.
type Frame struct {
	headers []Header
	payload []byte
}

func (f Frame) Payload() []byte { return append([]byte(nil), f.payload...) }
func (f Frame) Headers() []Header {
	out := append([]Header(nil), f.headers...)
	for i := range out {
		if b, ok := out[i].Value.([]byte); ok {
			out[i].Value = append([]byte(nil), b...)
		}
	}
	return out
}
func (f Frame) stringHeader(name string) (string, bool) {
	for _, h := range f.headers {
		if h.Name == name {
			s, ok := h.Value.(string)
			return s, ok
		}
	}
	return "", false
}

// Decoder reads incrementally from any chunked reader. EOF is clean only at a
// frame boundary, not proof of generation success. Every error is sticky:
// corrupted streams are never scanned for another plausible frame.
// A Decoder is single-consumer and is not safe for concurrent use.
type Decoder struct {
	r        io.Reader
	limits   Limits
	terminal error
}

func NewDecoder(r io.Reader, limits Limits) (*Decoder, error) {
	l, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, ErrRead
	}
	return &Decoder{r: r, limits: l}, nil
}
func (d *Decoder) fail(err error) (Frame, error) { d.terminal = err; return Frame{}, err }
func safeReadError(err error, started bool) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		if started || errors.Is(err, io.ErrUnexpectedEOF) {
			return io.ErrUnexpectedEOF
		}
		return io.EOF
	}
	// Do not echo arbitrary reader errors: they can contain credentials or bodies.
	return ErrRead
}
func (d *Decoder) Decode() (Frame, error) {
	if d.terminal != nil {
		return Frame{}, d.terminal
	}
	var p [12]byte
	n, err := io.ReadFull(d.r, p[:])
	if err != nil {
		return d.fail(safeReadError(err, n > 0))
	}
	if crc32.ChecksumIEEE(p[:8]) != binary.BigEndian.Uint32(p[8:]) {
		return d.fail(ErrFrame)
	}
	total, hl := uint64(binary.BigEndian.Uint32(p[:4])), uint64(binary.BigEndian.Uint32(p[4:8]))
	if total < 16 || hl > total-16 {
		return d.fail(ErrFrame)
	}
	if total > uint64(d.limits.MaxFrameBytes) || total > uint64(d.limits.MaxBufferBytes) || hl > uint64(d.limits.MaxHeadersBytes) {
		return d.fail(ErrLimit)
	}
	body := make([]byte, int(total)-12)
	if _, err = io.ReadFull(d.r, body); err != nil {
		return d.fail(safeReadError(err, true))
	}
	crc := crc32.Update(0, crc32.IEEETable, p[:])
	crc = crc32.Update(crc, crc32.IEEETable, body[:len(body)-4])
	if crc != binary.BigEndian.Uint32(body[len(body)-4:]) {
		return d.fail(ErrFrame)
	}
	headers, err := parseHeaders(body[:int(hl)])
	if err != nil {
		return d.fail(err)
	}
	return Frame{headers: headers, payload: body[int(hl) : len(body)-4]}, nil
}

// Duplicate names are rejected, including identical duplicates. This prevents
// first/last-value ambiguity for routing and exception headers.
func parseHeaders(b []byte) ([]Header, error) {
	var out []Header
	seen := make(map[string]bool)
	for len(b) > 0 {
		n := int(b[0])
		b = b[1:]
		if n == 0 || len(b) < n+1 || !utf8.Valid(b[:n]) {
			return nil, ErrHeader
		}
		name := string(b[:n])
		typ := b[n]
		b = b[n+1:]
		if seen[name] {
			return nil, ErrHeader
		}
		seen[name] = true
		size := 0
		switch typ {
		case 0, 1:
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8:
			size = 8
		case 6, 7:
			if len(b) < 2 {
				return nil, ErrHeader
			}
			size = int(binary.BigEndian.Uint16(b))
			b = b[2:]
		case 9:
			size = 16
		default:
			return nil, ErrHeader
		}
		if len(b) < size {
			return nil, ErrHeader
		}
		v := b[:size]
		var value any
		switch typ {
		case 0, 1:
			value = typ == 0
		case 2:
			value = int8(v[0])
		case 3:
			value = int16(binary.BigEndian.Uint16(v))
		case 4:
			value = int32(binary.BigEndian.Uint32(v))
		case 5, 8:
			value = int64(binary.BigEndian.Uint64(v))
		case 6:
			value = append([]byte(nil), v...)
		case 7:
			if !utf8.Valid(v) {
				return nil, ErrHeader
			}
			value = string(v)
		case 9:
			var id [16]byte
			copy(id[:], v)
			value = id
		}
		out = append(out, Header{Name: name, Type: typ, Value: value})
		b = b[size:]
	}
	return out, nil
}
