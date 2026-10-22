package locales

import "testing"

func TestTextIn(t *testing.T) {
	tests := []struct{ locale, text, want string }{
		{"en", "我的连接", "My Connections"},
		{"en-AU", "新建连接…", "New Connection…"},
		{"zh-Hans", "我的连接", "我的连接"},
		{"en", "user_database", "user_database"},
		{"en", "", ""},
	}
	for _, tc := range tests {
		if got := TextIn(tc.locale, tc.text); got != tc.want {
			t.Errorf("TextIn(%q, %q) = %q, want %q", tc.locale, tc.text, got, tc.want)
		}
	}
}
func TestCatalogLanguages(t *testing.T) {
	langs := Catalog.Languages()
	if len(langs) != 2 {
		t.Fatalf("catalog languages: %v", langs)
	}
	if got := In("en").MyConnections(); got != "My Connections" {
		t.Fatalf("generated English message = %q", got)
	}
}
