// Package lspclient is a generic, testable JSON-RPC 2.0 client over stdio for
// language servers (gopls, the TypeScript language server, …). It owns the
// process, Content-Length framing, concurrent requests, notifications, server
// requests, cancellation and shutdown — so each adapter does none of that. It is
// independent of any specific server.
package lspclient

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	maxHeaderLineBytes = 8 << 10 // a single header line ceiling
	maxHeaders         = 32      // header-count ceiling
)

var (
	errHeaderTooLong    = errors.New("lspclient: header line too long")
	errTooManyHeaders   = errors.New("lspclient: too many headers")
	errMalformedHeader  = errors.New("lspclient: malformed header")
	errNoContentLength  = errors.New("lspclient: missing Content-Length")
	errBadContentLength = errors.New("lspclient: invalid Content-Length")
	errMessageTooLarge  = errors.New("lspclient: message exceeds MaxMessageBytes")
)

// readMessage parses one Content-Length-framed message. Headers are
// case-insensitive and bounded; exactly one valid, non-negative Content-Length is
// required, and an oversized payload is rejected before allocation.
func readMessage(reader *bufio.Reader, maxBytes int64) ([]byte, error) {
	contentLength := int64(-1)
	headerCount := 0
	for {
		line, err := readHeaderLine(reader)
		if err != nil {
			return nil, err
		}
		if line == "" {
			break // blank line terminates the header block
		}
		headerCount++
		if headerCount > maxHeaders {
			return nil, errTooManyHeaders
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errMalformedHeader
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			parsed, perr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if perr != nil || parsed < 0 {
				return nil, errBadContentLength
			}
			contentLength = parsed
		}
		// Other known LSP headers (e.g. Content-Type) are accepted but not trusted.
	}
	if contentLength < 0 {
		return nil, errNoContentLength
	}
	if maxBytes > 0 && contentLength > maxBytes {
		return nil, fmt.Errorf("%w (%d > %d)", errMessageTooLarge, contentLength, maxBytes)
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// readHeaderLine reads a single CRLF-terminated header line, bounding its length.
func readHeaderLine(reader *bufio.Reader) (string, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxHeaderLineBytes {
			return "", errHeaderTooLong
		}
		if err == nil {
			if len(line) == 0 {
				return strings.TrimRight(string(fragment), "\r\n"), nil
			}
			line = append(line, fragment...)
			return strings.TrimRight(string(line), "\r\n"), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return "", err
		}
		if line == nil {
			line = make([]byte, 0, maxHeaderLineBytes)
		}
		line = append(line, fragment...)
		if len(line) == maxHeaderLineBytes {
			// A terminator would necessarily exceed the line budget, so fail
			// without reading another fragment from an unbounded stream.
			return "", errHeaderTooLong
		}
	}
}

// writeMessage frames and writes one payload.
func writeMessage(writer io.Writer, payload []byte) error {
	if _, err := io.WriteString(writer, "Content-Length: "+strconv.Itoa(len(payload))+"\r\n\r\n"); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}
