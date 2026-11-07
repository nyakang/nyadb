package sqlworkspace

import "github.com/nyakang/nyadb/locales"

func (d *Document) restRequests() bool {
	return d.commandDialect == "elasticsearch" || d.commandDialect == "easysearch"
}

func (d *Document) commandText(mongo, redis, elasticsearch, easysearch string) string {
	if d.commandDialect == "easysearch" {
		return locales.Text(easysearch)
	}
	if d.commandDialect == "elasticsearch" {
		return locales.Text(elasticsearch)
	}
	if d.commandDialect == "redis" {
		return locales.Text(redis)
	}
	return locales.Text(mongo)
}
