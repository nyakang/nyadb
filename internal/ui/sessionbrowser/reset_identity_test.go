package sessionbrowser

import "testing"

func TestDisconnectResetCarriesOriginalProfile(t *testing.T) {
	var reset int64
	s := New(Dependencies{BrowserReset: func(id int64) { reset = id }})
	s.current.ProfileID = 7
	s.current.Status = "已连接"
	s.Disconnect(7)
	if reset != 7 || s.current.ProfileID != 0 || s.current.Status != "未连接" {
		t.Fatal("disconnect reset lost original identity")
	}
}
