package mongoimport

import (
	"bufio"
	"fmt"
	"gopkg.in/mgo.v2/bson"
	"io"
	"strings"
)

const (
	entryDelimiter = '\n'
	tokenSeparator = "\t"
)

// TSVInputReader is a struct that implements the InputReader interface for a
// TSV input source
type TSVInputReader struct {
	// fields is a list of field names in the BSON documents to be imported
	fields []string

	// tsvReader is the underlying reader used to read data in from the TSV
	// or TSV file
	tsvReader *bufio.Reader

	// tsvRecord stores each line of input we read from the underlying reader
	tsvRecord string

	// numProcessed tracks the number of TSV records processed by the underlying reader
	numProcessed uint64

	// numDecoders is the number of concurrent goroutines to use for decoding
	numDecoders int

	// embedded sizeTracker exposes the Size() method to check the number of bytes read so far
	sizeTracker
}

// TSVConverter implements the Converter interface for TSV input
type TSVConverter struct {
	fields []string
	data   string
	index  uint64
}

// NewTSVInputReader returns a TSVInputReader configured to read input from the
// given io.Reader, extracting the specified fields only.
func NewTSVInputReader(fields []string, in io.Reader, numDecoders int) *TSVInputReader {
	szCount := &sizeTrackingReader{in, 0}
	return &TSVInputReader{
		fields:       fields,
		tsvReader:    bufio.NewReader(in),
		numProcessed: uint64(0),
		numDecoders:  numDecoders,
		sizeTracker:  szCount,
	}
}

// ReadAndValidateHeader sets the import fields for a TSV importer
func (tsvInputReader *TSVInputReader) ReadAndValidateHeader() (err error) {
	header, err := tsvInputReader.tsvReader.ReadString(entryDelimiter)
	if err != nil {
		return err
	}
	for _, field := range strings.Split(header, tokenSeparator) {
		tsvInputReader.fields = append(tsvInputReader.fields, strings.TrimRight(field, "\r\n"))
	}
	return validateReaderFields(tsvInputReader.fields)
}

// StreamDocument takes a boolean indicating if the documents should be streamed
// in read order and a channel on which to stream the documents processed from
// the underlying reader. Returns a non-nil error if encountered
func (tsvInputReader *TSVInputReader) StreamDocument(ordered bool, readDocChan chan bson.D) (retErr error) {
	tsvRecordChan := make(chan Converter, tsvInputReader.numDecoders)
	tsvErrChan := make(chan error)

	// begin reading from source
	go func() {
		var err error
		for {
			tsvInputReader.tsvRecord, err = tsvInputReader.tsvReader.ReadString(entryDelimiter)
			if err != nil {
				close(tsvRecordChan)
				if err == io.EOF {
					tsvErrChan <- nil
				} else {
					tsvInputReader.numProcessed++
					tsvErrChan <- fmt.Errorf("read error on entry #%v: %v", tsvInputReader.numProcessed, err)
				}
				return
			}
			tsvRecordChan <- TSVConverter{
				fields: tsvInputReader.fields,
				data:   tsvInputReader.tsvRecord,
				index:  tsvInputReader.numProcessed,
			}
			tsvInputReader.numProcessed++
		}
	}()

	// begin processing read bytes
	go func() {
		tsvErrChan <- streamDocuments(ordered, tsvInputReader.numDecoders, tsvRecordChan, readDocChan)
	}()

	return channelQuorumError(tsvErrChan, 2)
}

// This is required to satisfy the Converter interface for TSV input. It
// does TSV-specific processing to convert the TSVConverter struct to a bson.D
func (t TSVConverter) Convert() (bson.D, error) {
	return tokensToBSON(
		t.fields,
		strings.Split(strings.TrimRight(t.data, "\r\n"), tokenSeparator),
		t.index,
	)
}
