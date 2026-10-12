// Package imports stages, validates, and commits CSV imports.
package imports

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxColumns     = 64
	maxHeaderBytes = 64 << 10
)

// Reader streams CSV records after the header. It never buffers more than
// one record, so large files are read in constant memory.
type Reader struct {
	csv       *csv.Reader
	header    []string
	delimiter rune
	// pending holds the first line of a file without a header row; Next
	// returns it before reading on.
	pending []string
}

// A header names columns, so a first line with a date or a decimal amount is
// a record and the file has no header row.
var (
	dateLike   = regexp.MustCompile(`^\d{1,4}[-./]\d{1,2}[-./]\d{1,4}([ T].*)?$`)
	amountLike = regexp.MustCompile(`^[-+]?\d[\d ,.']*[.,]\d{1,2}$`)
)

func looksLikeRecord(line []string) bool {
	for _, value := range line {
		value = strings.TrimSpace(value)
		if dateLike.MatchString(value) || amountLike.MatchString(value) {
			return true
		}
	}
	return false
}

// NewReader strips a UTF-8 byte order mark, detects the delimiter from the
// header line, and reads the header. Quoted fields may contain delimiters,
// quotes, and newlines. A file without a header row gets the names Column 1,
// Column 2, and so on, and its first line stays a record.
func NewReader(source io.Reader) (*Reader, error) {
	buffered := bufio.NewReaderSize(source, maxHeaderBytes)
	if bom, err := buffered.Peek(3); err == nil && bytes.Equal(bom, []byte{0xEF, 0xBB, 0xBF}) {
		buffered.Discard(3)
	}
	sample, err := buffered.Peek(maxHeaderBytes)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, err
	}
	delimiter := sniffDelimiter(sample)
	reader := csv.NewReader(buffered)
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, userError("The file is empty")
	}
	if err != nil {
		return nil, err
	}
	var pending []string
	if looksLikeRecord(header) {
		if err := checkUTF8(header, 1); err != nil {
			return nil, err
		}
		pending = header
		header = make([]string, len(pending))
		for index := range header {
			header[index] = fmt.Sprintf("Column %d", index+1)
		}
	}
	if len(header) > maxColumns {
		return nil, userError(fmt.Sprintf("The file has more than %d columns", maxColumns))
	}
	for index, name := range header {
		if !utf8.ValidString(name) {
			return nil, userError("The file is not UTF-8 text")
		}
		header[index] = strings.TrimSpace(name)
	}
	if len(header) < 2 {
		return nil, userError("The header has fewer than two columns; check the delimiter")
	}
	return &Reader{csv: reader, header: header, delimiter: delimiter, pending: pending}, nil
}

// sniffDelimiter picks the most frequent candidate outside quotes on the
// first line. Commas win ties.
func sniffDelimiter(sample []byte) rune {
	counts := map[rune]int{}
	quoted := false
	for _, character := range string(sample) {
		if character == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && (character == '\n' || character == '\r') {
			break
		}
		if !quoted && (character == ',' || character == ';' || character == '\t') {
			counts[character]++
		}
	}
	best := ','
	for _, candidate := range []rune{';', '\t'} {
		if counts[candidate] > counts[best] {
			best = candidate
		}
	}
	return best
}

func (r *Reader) Header() []string { return r.header }

// Fingerprint identifies the format by its delimiter and normalized column
// names, so the same export layout reuses its cached mapping.
func (r *Reader) Fingerprint() string {
	names := make([]string, len(r.header))
	for index, name := range r.header {
		names[index] = strings.ToLower(strings.Join(strings.Fields(name), " "))
	}
	sum := sha256.Sum256([]byte(string(r.delimiter) + "\x00" + strings.Join(names, "\x1f")))
	return hex.EncodeToString(sum[:16])
}

// Next returns the next record, or io.EOF after the last one. Parse errors
// report only the line and column, never the content.
func (r *Reader) Next() ([]string, error) {
	if record := r.pending; record != nil {
		r.pending = nil
		return record, nil
	}
	record, err := r.csv.Read()
	if err != nil {
		var parse *csv.ParseError
		if errors.As(err, &parse) {
			return nil, userError(fmt.Sprintf("Line %d, column %d: %s", parse.Line, parse.Column, parse.Err))
		}
		return nil, err
	}
	if err := checkUTF8(record, r.line()); err != nil {
		return nil, err
	}
	return record, nil
}

func checkUTF8(record []string, line int) error {
	for _, value := range record {
		if !utf8.ValidString(value) {
			return userError(fmt.Sprintf("Line %d is not UTF-8 text", line))
		}
	}
	return nil
}

func (r *Reader) line() int {
	line, _ := r.csv.FieldPos(0)
	return line
}
