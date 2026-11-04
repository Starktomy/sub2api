package kiro

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"reflect"
	"strings"
	"testing"
)

func header(name string, typ byte, v []byte) []byte {
	b := append([]byte{byte(len(name))}, []byte(name)...)
	b = append(b, typ)
	if typ == 6 || typ == 7 {
		b = binary.BigEndian.AppendUint16(b, uint16(len(v)))
	}
	return append(b, v...)
}
func frame(h, p []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(16+len(h)+len(p)))
	b = binary.BigEndian.AppendUint32(b, uint32(len(h)))
	b = binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
	b = append(b, h...)
	b = append(b, p...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}
func eventFrame(mt, et, p string) []byte {
	h := header(":message-type", 7, []byte(mt))
	if et != "" {
		h = append(h, header(":event-type", 7, []byte(et))...)
	}
	return frame(h, []byte(p))
}

type chunkReader struct {
	b []byte
	n int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.n, len(r.b))
	copy(p, r.b[:n])
	r.b = r.b[n:]
	return n, nil
}
func decoder(t *testing.T, r io.Reader, l Limits) *Decoder {
	t.Helper()
	d, err := NewDecoder(r, l)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestChunkingAndOwnership(t *testing.T) {
	wire := eventFrame("event", "assistantResponseEvent", `{"content":"你好"}`)
	for n := 1; n <= len(wire); n++ {
		d := decoder(t, &chunkReader{b: append(append([]byte(nil), wire...), wire...), n: n}, Limits{})
		for j := 0; j < 2; j++ {
			e, err := d.NextEvent(0)
			if err != nil {
				t.Fatal(n, err)
			}
			v, ok := e.Assistant()
			if !ok || v.Content != "你好" {
				t.Fatal(v)
			}
			p := e.Raw()
			p[0] = '!'
			if e.Raw()[0] != '{' {
				t.Fatal("alias")
			}
		}
		if _, err := d.Decode(); err != io.EOF {
			t.Fatal(err)
		}
	}
	d := decoder(t, bytes.NewReader(frame(header("bytes", 6, []byte{1}), []byte("body"))), Limits{})
	f, err := d.Decode()
	if err != nil {
		t.Fatal(err)
	}
	h := f.Headers()
	h[0].Value.([]byte)[0] = 9
	p := f.Payload()
	p[0] = '!'
	if f.Headers()[0].Value.([]byte)[0] != 1 || string(f.Payload()) != "body" {
		t.Fatal("frame alias")
	}
}
func TestTruncationAndTerminal(t *testing.T) {
	wire := eventFrame("event", "future", `{}`)
	for n := 0; n < len(wire); n++ {
		d := decoder(t, bytes.NewReader(wire[:n]), Limits{})
		_, err := d.Decode()
		want := io.ErrUnexpectedEOF
		if n == 0 {
			want = io.EOF
		}
		if err != want {
			t.Fatalf("%d: %v", n, err)
		}
		if _, again := d.Decode(); again != err {
			t.Fatal("not sticky")
		}
	}
	for _, pos := range []int{0, 8, 12, len(wire) - 1} {
		bad := append([]byte(nil), wire...)
		bad[pos] ^= 1
		bad = append(bad, wire...)
		d := decoder(t, bytes.NewReader(bad), Limits{})
		if _, err := d.Decode(); err != ErrFrame {
			t.Fatal(err)
		}
		if _, err := d.Decode(); err != ErrFrame {
			t.Fatal("recovered")
		}
	}
}
func TestHeaderTypesAndBoundaries(t *testing.T) {
	values := [][]byte{nil, nil, {255}, {255, 255}, {255, 255, 255, 255}, {255, 255, 255, 255, 255, 255, 255, 255}, {1, 2}, []byte("text"), {0, 0, 0, 0, 0, 0, 0, 1}, make([]byte, 16)}
	want := []any{true, false, int8(-1), int16(-1), int32(-1), int64(-1), []byte{1, 2}, "text", int64(1), [16]byte{}}
	var all []byte
	for typ, v := range values {
		h := header(string(rune('a'+typ)), byte(typ), v)
		all = append(all, h...)
		for n := 1; n < len(h); n++ {
			if _, err := parseHeaders(h[:n]); err != ErrHeader {
				t.Fatalf("type %d cut %d", typ, n)
			}
		}
	}
	got, err := parseHeaders(all)
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range got {
		if !reflect.DeepEqual(h.Value, want[i]) {
			t.Fatal(i, h.Value)
		}
	}
	malformed := [][]byte{{0}, {1, 'x'}, {1, 'x', 10}, {1, 255, 0}, {1, 'x', 7, 0, 1, 255}, append(header("x", 0, nil), header("x", 1, nil)...)}
	for _, h := range malformed {
		if _, err := decoder(t, bytes.NewReader(frame(h, nil)), Limits{}).Decode(); err != ErrHeader {
			t.Fatal(h, err)
		}
	}
	// Maximum uint16 value length and uint8 header-name length are legal.
	h := header(strings.Repeat("n", 255), 7, bytes.Repeat([]byte("v"), 65535))
	if _, err := parseHeaders(h); err != nil {
		t.Fatal(err)
	}
}
func TestLimits(t *testing.T) {
	wire := frame(nil, []byte("payload"))
	for _, l := range []Limits{{MaxFrameBytes: len(wire) - 1}, {MaxBufferBytes: len(wire) - 1}} {
		if _, err := decoder(t, bytes.NewReader(wire[:12]), l).Decode(); err != ErrLimit {
			t.Fatal(err)
		}
	}
	if _, err := decoder(t, bytes.NewReader(wire), Limits{MaxFrameBytes: len(wire), MaxBufferBytes: len(wire)}).Decode(); err != nil {
		t.Fatal(err)
	}
	h := header("a", 0, nil)
	if _, err := decoder(t, bytes.NewReader(frame(h, nil)), Limits{MaxHeadersBytes: len(h) - 1}).Decode(); err != ErrLimit {
		t.Fatal(err)
	}
	for _, pair := range [][2]uint32{{15, 0}, {16, 1}, {^uint32(0), 0}} {
		p := binary.BigEndian.AppendUint32(nil, pair[0])
		p = binary.BigEndian.AppendUint32(p, pair[1])
		p = binary.BigEndian.AppendUint32(p, crc32.ChecksumIEEE(p))
		_, err := decoder(t, bytes.NewReader(p), Limits{}).Decode()
		if err != ErrFrame && err != ErrLimit {
			t.Fatal(err)
		}
	}
	if _, err := NewDecoder(bytes.NewReader(nil), Limits{MaxBufferBytes: -1}); err != ErrLimit {
		t.Fatal(err)
	}
}

type secretReader struct{}

func (secretReader) Read([]byte) (int, error) { return 0, errors.New("synthetic-secret-reader") }
func TestSafeErrors(t *testing.T) {
	_, err := decoder(t, secretReader{}, Limits{}).Decode()
	if err != ErrRead || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	for _, mt := range []string{"error", "exception"} {
		e, err := decoder(t, strings.NewReader(string(eventFrame(mt, "", `synthetic-secret-payload`))), Limits{}).NextEvent(0)
		if err != nil {
			t.Fatal(err)
		}
		var a Attempt
		_, err = a.Apply(e)
		if err != ErrUpstream || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
		if string(e.Raw()) != "synthetic-secret-payload" {
			t.Fatal("lost raw")
		}
	}
}
func FuzzDecoder(f *testing.F) {
	f.Add(eventFrame("event", "assistantResponseEvent", `{"content":"hi"}`))
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, b []byte) {
		d := decoder(t, bytes.NewReader(b), Limits{MaxFrameBytes: 4096, MaxBufferBytes: 4096, MaxHeadersBytes: 1024})
		for i := 0; i <= len(b)/16; i++ {
			if _, err := d.NextEvent(2048); err != nil {
				return
			}
		}
	})
}

func TestCRC32CIsRejected(t *testing.T) {
	wire := frame(nil, []byte("synthetic"))
	table := crc32.MakeTable(crc32.Castagnoli)
	binary.BigEndian.PutUint32(wire[8:12], crc32.Checksum(wire[:8], table))
	binary.BigEndian.PutUint32(wire[len(wire)-4:], crc32.Checksum(wire[:len(wire)-4], table))
	if _, err := decoder(t, bytes.NewReader(wire), Limits{}).Decode(); err != ErrFrame {
		t.Fatal(err)
	}
}
func TestEveryTwoChunkSplit(t *testing.T) {
	wire := append(eventFrame("event", "unknown", "first"), eventFrame("event", "unknown", "second")...)
	for split := 0; split <= len(wire); split++ {
		r := io.MultiReader(bytes.NewReader(wire[:split]), bytes.NewReader(wire[split:]))
		d := decoder(t, r, Limits{})
		for _, want := range []string{"first", "second"} {
			e, err := d.NextEvent(0)
			if err != nil || string(e.Raw()) != want {
				t.Fatal(split, err)
			}
		}
	}
}
