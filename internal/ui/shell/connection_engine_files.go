package shell

import "github.com/nyakang/nyadb/internal/ui/connectioneditor"

type EngineFile = connectioneditor.EngineFile

const (
	SQLiteAttachedFile        = connectioneditor.SQLiteAttachedFile
	SQLiteMainFile            = connectioneditor.SQLiteMainFile
	DuckDBMainFile            = connectioneditor.DuckDBMainFile
	DuckDBLibraryFile         = connectioneditor.DuckDBLibraryFile
	PostgreSQLCAFile          = connectioneditor.PostgreSQLCAFile
	PostgreSQLCertificateFile = connectioneditor.PostgreSQLCertificateFile
	PostgreSQLKeyFile         = connectioneditor.PostgreSQLKeyFile
)

func (s *State) SetEngineFilePicker(pick func(EngineFile, int, uint64)) {
	s.connectionEditor.SetEngineFilePicker(pick)
}
func (s *State) EngineFileVersion() uint64 { return s.connectionEditor.EngineFileVersion() }
func (s *State) SelectEngineFile(kind EngineFile, row int, version uint64, path string) {
	s.connectionEditor.SelectEngineFile(kind, row, version, path)
}
func (s *State) EngineFilePickerError(version uint64) {
	s.connectionEditor.EngineFilePickerError(version)
}
