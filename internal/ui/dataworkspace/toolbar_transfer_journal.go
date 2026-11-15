package dataworkspace

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"os"
	"sync"
)

// Both records and offsets stay on disk. A visible grid row performs one
// bounded indexed read. Provisional status is settled by the final receipt.
type mysqlDataImportJournal struct {
	mu                    sync.Mutex
	once                  sync.Once
	data, index, receipts *os.File
	count, total          int64
	offset                int64
	receiptBytes          int64
	exports               int
	closed                bool
}

func newMySQLDataImportJournal(owner *mysqlDataSpoolOwner, total int64) (*mysqlDataImportJournal, error) {
	data, err := os.CreateTemp("", "nyadb-import-results-*.jsonl")
	if err != nil {
		return nil, err
	}
	index, err := os.CreateTemp("", "nyadb-import-offsets-*.bin")
	if err != nil {
		data.Close()
		os.Remove(data.Name())
		return nil, err
	}
	receipts, err := os.CreateTemp("", "nyadb-import-receipts-*.jsonl")
	if err != nil {
		data.Close()
		index.Close()
		os.Remove(data.Name())
		os.Remove(index.Name())
		return nil, err
	}
	j := &mysqlDataImportJournal{data: data, index: index, receipts: receipts, total: total}
	if err = owner.add(&mysqlDataImportSpool{path: data.Name()}); err == nil {
		err = owner.add(&mysqlDataImportSpool{path: index.Name()})
	}
	if err == nil {
		err = owner.add(&mysqlDataImportSpool{path: receipts.Name()})
	}
	if err != nil {
		j.Close()
		return nil, err
	}
	if owner != nil {
		owner.mu.Lock()
		if owner.closed {
			owner.mu.Unlock()
			j.Close()
			return nil, context.Canceled
		}
		owner.cleanup = append(owner.cleanup, j.Close)
		owner.mu.Unlock()
	}
	return j, nil
}
func (j *mysqlDataImportJournal) Record(row contract.DataImportRowResult) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return context.Canceled
	}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	offset := make([]byte, 8)
	binary.LittleEndian.PutUint64(offset, uint64(j.offset))
	if _, err = j.data.Write(data); err != nil {
		return err
	}
	if _, err = j.index.Write(offset); err != nil {
		return err
	}
	j.offset += int64(len(data))
	j.count++
	return nil
}
func (j *mysqlDataImportJournal) Flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return context.Canceled
	}
	if err := j.data.Sync(); err != nil {
		return err
	}
	return errors.Join(j.index.Sync(), j.receipts.Sync())
}
func (j *mysqlDataImportJournal) Close() error {
	if j == nil {
		return nil
	}
	var err error
	j.once.Do(func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.closed = true
		err = errors.Join(j.data.Close(), j.index.Close())
		if j.exports == 0 {
			err = errors.Join(err, j.receipts.Close())
		}
	})
	return err
}
func (j *mysqlDataImportJournal) Row(index int64, committed, uncertain bool) (contract.DataImportRowResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return contract.DataImportRowResult{}, context.Canceled
	}
	row := contract.DataImportRowResult{Row: int(index + 1), Status: "not_processed"}
	if index < 0 || index >= j.total {
		return row, io.EOF
	}
	if index >= j.count {
		return row, nil
	}
	offset := make([]byte, 8)
	if _, err := j.index.ReadAt(offset, index*8); err != nil {
		return row, err
	}
	start := int64(binary.LittleEndian.Uint64(offset))
	reader := bufio.NewReader(io.NewSectionReader(j.data, start, 8<<20))
	data, err := reader.ReadBytes('\n')
	if err != nil {
		return row, err
	}
	if err = json.Unmarshal(data, &row); err != nil {
		return row, err
	}
	if row.Status == "prepared" {
		switch {
		case uncertain:
			row.Status = "uncertain"
		case committed:
			row.Status = "committed"
		default:
			row.Status = "rolled_back"
		}
	}
	return row, nil
}

type mysqlDataImportReceiptRecord struct {
	SourceIndex  int
	SourceOffset int64
	Receipt      contract.DataImportBatchReceipt
}

// Append original provider receipts independently of projected row outcomes.
// A sync failure never erases the acknowledged result returned by the provider.
func (j *mysqlDataImportJournal) RecordReceipt(source int, offset int64, receipt contract.DataImportBatchReceipt) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return context.Canceled
	}
	data, err := json.Marshal(mysqlDataImportReceiptRecord{source, offset, receipt})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	n, err := j.receipts.Write(data)
	if err != nil || n != len(data) {
		// Preserve the complete prefix; the provider's LastReceipt keeps the
		// acknowledged current batch even if this archive cannot store it.
		err = errors.Join(err, j.receipts.Truncate(j.receiptBytes))
		_, seekErr := j.receipts.Seek(j.receiptBytes, io.SeekStart)
		return errors.Join(err, seekErr, io.ErrShortWrite)
	}
	j.receiptBytes += int64(n)
	return j.receipts.Sync()
}

// The original descriptor and length are captured before the native picker.
// Close may release other assets, but this descriptor lives through its reader.
type mysqlDataImportReceiptSnapshot struct {
	*io.SectionReader
	journal *mysqlDataImportJournal
	once    sync.Once
}

func (j *mysqlDataImportJournal) receiptSnapshot() (*mysqlDataImportReceiptSnapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, context.Canceled
	}
	if j.receiptBytes == 0 {
		return nil, io.EOF
	}
	j.exports++
	return &mysqlDataImportReceiptSnapshot{SectionReader: io.NewSectionReader(j.receipts, 0, j.receiptBytes), journal: j}, nil
}
func (s *mysqlDataImportReceiptSnapshot) Close() error {
	var err error
	s.once.Do(func() {
		j := s.journal
		j.mu.Lock()
		defer j.mu.Unlock()
		j.exports--
		if j.closed && j.exports == 0 {
			err = j.receipts.Close()
			removeErr := os.Remove(j.receipts.Name())
			if !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	})
	return err
}
