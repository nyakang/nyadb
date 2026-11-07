package sqlworkspace

import "github.com/nyakang/nyadb/locales"

func (d *Document) commandText(mongo, redis, elasticsearch string) string {
	if d.commandDialect == "elasticsearch" {
		return locales.Text(elasticsearch)
	}
	if d.commandDialect == "redis" {
		return locales.Text(redis)
	}
	return locales.Text(mongo)
}
