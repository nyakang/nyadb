package sqlworkspace

import "github.com/nyakang/nyadb/locales"

func (d *Document) commandText(mongo, redis string) string {
	if d.commandDialect == "redis" {
		return locales.Text(redis)
	}
	return locales.Text(mongo)
}
