package locales

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/egoist/mygo/i18n"
)

// sourceKeys indexes the exact source-language phrases in the generated ARB
// catalog. Never use this to translate database names or user-entered text.
var sourceKeys = loadSourceKeys()

func loadSourceKeys() map[string]string {
	data, err := files.ReadFile("zh-Hans.json")
	if err != nil {
		panic(err)
	}
	messages := map[string]string{}
	if err := json.Unmarshal(data, &messages); err != nil {
		panic(err)
	}
	keys := make([]string, 0, len(messages))
	for key := range messages {
		if !strings.HasPrefix(key, "@") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	byText := make(map[string]string, len(keys))
	for _, key := range keys {
		source := messages[key]
		if _, exists := byText[source]; !exists {
			byText[source] = key
		}
	}
	return byText
}

// Text translates a static UI phrase from the zh-Hans source catalog.
// Unknown phrases are left unchanged so that names and fallback UI remain
// safe. For new strings prefer the generated typed accessors where possible.
func Text(source string) string { return TextIn(i18n.Locale(), source) }

// TextIn translates a static UI phrase in the requested language.
// It also makes translation independent from global App state in tests.
func TextIn(locale, source string) string {
	if key, ok := sourceKeys[source]; ok {
		return Catalog.Format(locale, key, nil)
	}
	return source
}
