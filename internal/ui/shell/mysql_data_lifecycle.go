package shell

// Closing a document cancels all of its independent readers. A normal data
// refresh only cancels the page reader, leaving table statistics undisturbed.
func (s *State) disposeMySQLDataTab(t *tab) {
	s.flushMySQLDataLayout(t)
	s.cancelMySQLDataEdit(t)
	s.cancelMySQLData(t)
	s.dataInspector.Close(t.dataPanel)
}
