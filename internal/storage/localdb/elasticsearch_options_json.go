package localdb

import (
	"encoding/json"
	"io"
	"strings"
)

// A duplicate authentication member must not silently change the meaning of a
// separate saved credential. Existing engines retain their historical formats.
func uniqueElasticsearchOptionsJSON(raw string) bool {
	if len(raw) == 0 || len(raw) > 128<<10 {
		return false
	}
	d := json.NewDecoder(strings.NewReader(raw))
	var read func(int) bool
	read = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, e := d.Token()
					name, ok := key.(string)
					name = strings.ToLower(name) // encoding/json also accepts case variants.
					if e != nil || !ok || seen[name] || !read(depth+1) {
						return false
					}
					seen[name] = true
				}
			case '[':
				for d.More() {
					if !read(depth + 1) {
						return false
					}
				}
			default:
				return false
			}
			if _, err = d.Token(); err != nil {
				return false
			}
		}
		return true
	}
	if !read(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
