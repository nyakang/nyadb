package dataworkspace

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// Both records and offsets stay on disk. A visible grid row performs one
// bounded indexed read. Provisional status is settled by the final receipt.
type mysqlDataImportJournal struct {
	once         sync.Once
	data, index  *os.File
	count, total int64
	offset       int64
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
	j := &mysqlDataImportJournal{data: data, index: index, total: total}
	if err = owner.add(&mysqlDataImportSpool{path: data.Name()}); err == nil {
		err = owner.add(&mysqlDataImportSpool{path: index.Name()})
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
func (j *mysqlDataImportJournal) Record(row mysqlsvc.MySQLDataImportRowResult) error {
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
	if err := j.data.Sync(); err != nil {
		return err
	}
	return j.index.Sync()
}
func (j *mysqlDataImportJournal) Close() error {
	if j == nil {
		return nil
	}
	var err error
	j.once.Do(func() { err = errors.Join(j.data.Close(), j.index.Close()) })
	return err
}
func (j *mysqlDataImportJournal) Row(index int64, committed, uncertain bool) (mysqlsvc.MySQLDataImportRowResult, error) {
	row := mysqlsvc.MySQLDataImportRowResult{Row: int(index + 1), Status: "not_processed"}
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
