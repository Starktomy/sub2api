package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExtractBedrockChunkData(t *testing.T) {
	t.Run("valid base64 payload", func(t *testing.T) {
		original := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`
		b64 := base64.StdEncoding.EncodeToString([]byte(original))
		payload := []byte(`{"bytes":"` + b64 + `"}`)

		result := extractBedrockChunkData(payload)
		require.NotNil(t, result)
		assert.JSONEq(t, original, string(result))
	})

	t.Run("empty bytes field", func(t *testing.T) {
		result := extractBedrockChunkData([]byte(`{"bytes":""}`))
		assert.Nil(t, result)
	})

	t.Run("no bytes field", func(t *testing.T) {
		result := extractBedrockChunkData([]byte(`{"other":"value"}`))
		assert.Nil(t, result)
	})

	t.Run("invalid base64", func(t *testing.T) {
		result := extractBedrockChunkData([]byte(`{"bytes":"not-valid-base64!!!"}`))
		assert.Nil(t, result)
	})
}

func TestTransformBedrockInvocationMetrics(t *testing.T) {
	t.Run("converts metrics to usage", func(t *testing.T) {
		input := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"amazon-bedrock-invocationMetrics":{"inputTokenCount":150,"outputTokenCount":42}}`
		result := transformBedrockInvocationMetrics([]byte(input))

		// amazon-bedrock-invocationMetrics should be removed
		assert.False(t, gjson.GetBytes(result, "amazon-bedrock-invocationMetrics").Exists())
		// usage should be set
		assert.Equal(t, int64(150), gjson.GetBytes(result, "usage.input_tokens").Int())
		assert.Equal(t, int64(42), gjson.GetBytes(result, "usage.output_tokens").Int())
		// original fields preserved
		assert.Equal(t, "message_delta", gjson.GetBytes(result, "type").String())
		assert.Equal(t, "end_turn", gjson.GetBytes(result, "delta.stop_reason").String())
	})

	t.Run("no metrics present", func(t *testing.T) {
		input := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`
		result := transformBedrockInvocationMetrics([]byte(input))
		assert.JSONEq(t, input, string(result))
	})

	t.Run("does not overwrite existing usage", func(t *testing.T) {
		input := `{"type":"message_delta","usage":{"output_tokens":100},"amazon-bedrock-invocationMetrics":{"inputTokenCount":150,"outputTokenCount":42}}`
		result := transformBedrockInvocationMetrics([]byte(input))

		// metrics removed but existing usage preserved
		assert.False(t, gjson.GetBytes(result, "amazon-bedrock-invocationMetrics").Exists())
		assert.Equal(t, int64(100), gjson.GetBytes(result, "usage.output_tokens").Int())
	})
}

func TestExtractEventStreamHeaderValue(t *testing.T) {
	// Build a header with :event-type = "chunk" (string type = 7)
	buildStringHeader := func(name, value string) []byte {
		var buf bytes.Buffer
		// name length (1 byte)
		_ = buf.WriteByte(byte(len(name)))
		// name
		_, _ = buf.WriteString(name)
		// value type (7 = string)
		_ = buf.WriteByte(7)
		// value length (2 bytes, big-endian)
		_ = binary.Write(&buf, binary.BigEndian, uint16(len(value)))
		// value
		_, _ = buf.WriteString(value)
		return buf.Bytes()
	}

	t.Run("find string header", func(t *testing.T) {
		headers := buildStringHeader(":event-type", "chunk")
		assert.Equal(t, "chunk", extractEventStreamHeaderValue(headers, ":event-type"))
	})

	t.Run("header not found", func(t *testing.T) {
		headers := buildStringHeader(":event-type", "chunk")
		assert.Equal(t, "", extractEventStreamHeaderValue(headers, ":message-type"))
	})

	t.Run("multiple headers", func(t *testing.T) {
		var buf bytes.Buffer
		_, _ = buf.Write(buildStringHeader(":content-type", "application/json"))
		_, _ = buf.Write(buildStringHeader(":event-type", "chunk"))
		_, _ = buf.Write(buildStringHeader(":message-type", "event"))

		headers := buf.Bytes()
		assert.Equal(t, "chunk", extractEventStreamHeaderValue(headers, ":event-type"))
		assert.Equal(t, "application/json", extractEventStreamHeaderValue(headers, ":content-type"))
		assert.Equal(t, "event", extractEventStreamHeaderValue(headers, ":message-type"))
	})

	t.Run("empty headers", func(t *testing.T) {
		assert.Equal(t, "", extractEventStreamHeaderValue([]byte{}, ":event-type"))
	})
}

func TestBedrockEventStreamDecoder(t *testing.T) {
	crc32IeeeTab := crc32.MakeTable(crc32.IEEE)

	// Build a valid EventStream frame with correct CRC32/IEEE checksums.
	buildFrame := func(eventType string, payload []byte) []byte {
		// Build headers
		var headersBuf bytes.Buffer
		// :event-type header
		_ = headersBuf.WriteByte(byte(len(":event-type")))
		_, _ = headersBuf.WriteString(":event-type")
		_ = headersBuf.WriteByte(7) // string type
		_ = binary.Write(&headersBuf, binary.BigEndian, uint16(len(eventType)))
		_, _ = headersBuf.WriteString(eventType)
		// :message-type header
		_ = headersBuf.WriteByte(byte(len(":message-type")))
		_, _ = headersBuf.WriteString(":message-type")
		_ = headersBuf.WriteByte(7)
		_ = binary.Write(&headersBuf, binary.BigEndian, uint16(len("event")))
		_, _ = headersBuf.WriteString("event")

		headers := headersBuf.Bytes()
		headersLen := uint32(len(headers))
		// total = 12 (prelude) + headers + payload + 4 (message_crc)
		totalLen := uint32(12 + len(headers) + len(payload) + 4)

		// Prelude: total_length(4) + headers_length(4)
		var preludeBuf bytes.Buffer
		_ = binary.Write(&preludeBuf, binary.BigEndian, totalLen)
		_ = binary.Write(&preludeBuf, binary.BigEndian, headersLen)
		preludeBytes := preludeBuf.Bytes()
		preludeCRC := crc32.Checksum(preludeBytes, crc32IeeeTab)

		// Build frame: prelude + prelude_crc + headers + payload
		var frame bytes.Buffer
		_, _ = frame.Write(preludeBytes)
		_ = binary.Write(&frame, binary.BigEndian, preludeCRC)
		_, _ = frame.Write(headers)
		_, _ = frame.Write(payload)

		// Message CRC covers everything before itself
		messageCRC := crc32.Checksum(frame.Bytes(), crc32IeeeTab)
		_ = binary.Write(&frame, binary.BigEndian, messageCRC)
		return frame.Bytes()
	}

	t.Run("decode chunk event", func(t *testing.T) {
		payload := []byte(`{"bytes":"dGVzdA=="}`) // base64("test")
		frame := buildFrame("chunk", payload)

		decoder := newBedrockEventStreamDecoder(bytes.NewReader(frame))
		result, err := decoder.Decode()
		require.NoError(t, err)
		assert.Equal(t, payload, result)
	})

	t.Run("skip non-chunk events", func(t *testing.T) {
		// Write initial-response followed by chunk
		var buf bytes.Buffer
		_, _ = buf.Write(buildFrame("initial-response", []byte(`{}`)))
		chunkPayload := []byte(`{"bytes":"aGVsbG8="}`)
		_, _ = buf.Write(buildFrame("chunk", chunkPayload))

		decoder := newBedrockEventStreamDecoder(&buf)
		result, err := decoder.Decode()
		require.NoError(t, err)
		assert.Equal(t, chunkPayload, result)
	})

	t.Run("EOF on empty input", func(t *testing.T) {
		decoder := newBedrockEventStreamDecoder(bytes.NewReader(nil))
		_, err := decoder.Decode()
		assert.Equal(t, io.EOF, err)
	})

	t.Run("corrupted prelude CRC", func(t *testing.T) {
		frame := buildFrame("chunk", []byte(`{"bytes":"dGVzdA=="}`))
		// Corrupt the prelude CRC (bytes 8-11)
		frame[8] ^= 0xFF
		decoder := newBedrockEventStreamDecoder(bytes.NewReader(frame))
		_, err := decoder.Decode()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "prelude CRC mismatch")
	})

	t.Run("corrupted message CRC", func(t *testing.T) {
		frame := buildFrame("chunk", []byte(`{"bytes":"dGVzdA=="}`))
		// Corrupt the message CRC (last 4 bytes)
		frame[len(frame)-1] ^= 0xFF
		decoder := newBedrockEventStreamDecoder(bytes.NewReader(frame))
		_, err := decoder.Decode()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "message CRC mismatch")
	})

	t.Run("castagnoli encoded frame is rejected", func(t *testing.T) {
		castagnoliTab := crc32.MakeTable(crc32.Castagnoli)
		payload := []byte(`{"bytes":"dGVzdA=="}`)

		var headersBuf bytes.Buffer
		_ = headersBuf.WriteByte(byte(len(":event-type")))
		_, _ = headersBuf.WriteString(":event-type")
		_ = headersBuf.WriteByte(7)
		_ = binary.Write(&headersBuf, binary.BigEndian, uint16(len("chunk")))
		_, _ = headersBuf.WriteString("chunk")

		headers := headersBuf.Bytes()
		headersLen := uint32(len(headers))
		totalLen := uint32(12 + len(headers) + len(payload) + 4)

		var preludeBuf bytes.Buffer
		_ = binary.Write(&preludeBuf, binary.BigEndian, totalLen)
		_ = binary.Write(&preludeBuf, binary.BigEndian, headersLen)
		preludeBytes := preludeBuf.Bytes()

		var frame bytes.Buffer
		_, _ = frame.Write(preludeBytes)
		_ = binary.Write(&frame, binary.BigEndian, crc32.Checksum(preludeBytes, castagnoliTab))
		_, _ = frame.Write(headers)
		_, _ = frame.Write(payload)
		_ = binary.Write(&frame, binary.BigEndian, crc32.Checksum(frame.Bytes(), castagnoliTab))

		decoder := newBedrockEventStreamDecoder(bytes.NewReader(frame.Bytes()))
		_, err := decoder.Decode()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "prelude CRC mismatch")
	})
}

func TestBuildBedrockURL(t *testing.T) {
	t.Run("stream URL with colon in model ID", func(t *testing.T) {
		url := BuildBedrockURL("us-east-1", "us.anthropic.claude-opus-4-5-20251101-v1:0", true)
		assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-opus-4-5-20251101-v1%3A0/invoke-with-response-stream", url)
	})

	t.Run("non-stream URL with colon in model ID", func(t *testing.T) {
		url := BuildBedrockURL("eu-west-1", "eu.anthropic.claude-sonnet-4-5-20250929-v1:0", false)
		assert.Equal(t, "https://bedrock-runtime.eu-west-1.amazonaws.com/model/eu.anthropic.claude-sonnet-4-5-20250929-v1%3A0/invoke", url)
	})

	t.Run("model ID without colon", func(t *testing.T) {
		url := BuildBedrockURL("us-east-1", "us.anthropic.claude-sonnet-4-6", true)
		assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-sonnet-4-6/invoke-with-response-stream", url)
	})
}

// bedrockTestPrelude builds CRC-valid length declarations without a body.
func bedrockTestPrelude(total, headers uint32) []byte {
	p := make([]byte, 12)
	binary.BigEndian.PutUint32(p, total)
	binary.BigEndian.PutUint32(p[4:], headers)
	binary.BigEndian.PutUint32(p[8:], crc32.ChecksumIEEE(p[:8]))
	return p
}

func bedrockTestRawFrame(headers, payload []byte) []byte {
	frame := bedrockTestPrelude(uint32(16+len(headers)+len(payload)), uint32(len(headers)))
	frame = append(frame, headers...)
	frame = append(frame, payload...)
	return binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
}

type bedrockPreludeOnlyReader struct {
	prelude  []byte
	bodyRead bool
}

func (r *bedrockPreludeOnlyReader) Read(p []byte) (int, error) {
	if len(r.prelude) == 0 {
		r.bodyRead = true
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.prelude)
	r.prelude = r.prelude[n:]
	return n, nil
}

func TestBedrockEventStreamLengthGuards(t *testing.T) {
	for _, tc := range []struct {
		name           string
		total, headers uint32
		want           string
	}{
		{"below minimum", 15, 0, "total_length"},
		{"oversized total", bedrockEventStreamMaxTotalLength + 1, 0, "total length exceeds limit"},
		{"uint32 max", ^uint32(0), 0, "total length exceeds limit"},
		{"headers overlap checksum", 16, 1, "headers exceed frame body"},
		{"headers exceed body", 32, ^uint32(0), "headers exceed frame body"},
		{"oversized headers", bedrockEventStreamMaxHeadersLength + 17, bedrockEventStreamMaxHeadersLength + 1, "headers length exceeds limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &bedrockPreludeOnlyReader{prelude: bedrockTestPrelude(tc.total, tc.headers)}
			_, err := newBedrockEventStreamDecoder(r).Decode()
			require.ErrorContains(t, err, tc.want)
			assert.False(t, r.bodyRead, "invalid prelude must be rejected before reading the body")
		})
	}
}

func TestBedrockEventStreamMalformedHeaders(t *testing.T) {
	chunk := append([]byte{11}, []byte(":event-type")...)
	chunk = append(chunk, 7, 0, 5)
	chunk = append(chunk, []byte("chunk")...)
	cases := [][]byte{
		{0}, {3, 'x'}, {1, 'x'}, {1, 'x', 255},
		{1, 'x', 6}, {1, 'x', 6, 0}, {1, 'x', 6, 0, 2, 'x'},
		{1, 'x', 7}, {1, 'x', 7, 0}, {1, 'x', 7, 0, 2, 'x'},
	}
	for _, typ := range []byte{2, 3, 4, 5, 8, 9} {
		cases = append(cases, []byte{1, 'x', typ})
	}
	for i, malformed := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			for _, prefix := range [][]byte{nil, chunk} {
				headers := append(append([]byte(nil), prefix...), malformed...)
				frame := bedrockTestRawFrame(headers, []byte("secret-upstream-payload"))
				_, err := newBedrockEventStreamDecoder(bytes.NewReader(frame)).Decode()
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "secret-upstream-payload")
				assert.Empty(t, extractEventStreamHeaderValue(headers, ":event-type"))
			}
		})
	}
}

func TestBedrockEventStreamTruncation(t *testing.T) {
	frame := bedrockTestRawFrame([]byte{1, 'x', 0}, []byte("payload"))
	for n := 1; n < len(frame); n++ {
		_, err := newBedrockEventStreamDecoder(bytes.NewReader(frame[:n])).Decode()
		require.ErrorIs(t, err, io.ErrUnexpectedEOF, "length %d", n)
	}
	_, err := newBedrockEventStreamDecoder(bytes.NewReader(nil)).Decode()
	require.ErrorIs(t, err, io.EOF)
}

func TestBedrockEventStreamValidHeaderTypes(t *testing.T) {
	var headers []byte
	for typ, size := range []int{0, 0, 1, 2, 4, 8, 3, 3, 8, 16} {
		headers = append(headers, 1, byte('a'+typ), byte(typ))
		if typ == 6 || typ == 7 {
			headers = append(headers, 0, byte(size))
		}
		headers = append(headers, make([]byte, size)...)
	}
	headers = append(headers, 11)
	headers = append(headers, []byte(":event-type")...)
	headers = append(headers, 7, 0, 5)
	headers = append(headers, []byte("chunk")...)
	frame := bedrockTestRawFrame(headers, []byte("valid"))
	decoder := newBedrockEventStreamDecoder(bytes.NewReader(append(frame, frame...)))
	for i := 0; i < 2; i++ {
		payload, err := decoder.Decode()
		require.NoError(t, err)
		assert.Equal(t, "valid", string(payload))
	}
	_, err := decoder.Decode()
	require.ErrorIs(t, err, io.EOF)
}

type bedrockErrorReader struct{ err error }

func (r bedrockErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestBedrockEventStreamCancellation(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, prefix := range [][]byte{nil, bedrockTestPrelude(32, 0)} {
			r := io.MultiReader(bytes.NewReader(prefix), bedrockErrorReader{want})
			_, err := newBedrockEventStreamDecoder(r).Decode()
			require.ErrorIs(t, err, want)
		}
	}
}

func TestBedrockEventStreamLengthBoundaries(t *testing.T) {
	// At the caps, the prelude must be accepted and body reading attempted.
	for _, lengths := range [][2]uint32{
		{16, 0},
		{bedrockEventStreamMaxTotalLength, bedrockEventStreamMaxHeadersLength},
	} {
		r := &bedrockPreludeOnlyReader{prelude: bedrockTestPrelude(lengths[0], lengths[1])}
		_, err := newBedrockEventStreamDecoder(r).Decode()
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		assert.True(t, r.bodyRead)
	}
	// A valid minimum frame is skipped, not rejected.
	_, err := newBedrockEventStreamDecoder(bytes.NewReader(bedrockTestRawFrame(nil, nil))).Decode()
	require.ErrorIs(t, err, io.EOF)
}
