package sqlworkspace

import "github.com/nyakang/nyadb/locales"

func (d *Document) restRequests() bool {
	return d.commandDialect == "elasticsearch" || d.commandDialect == "easysearch" || d.commandDialect == "solr" || d.commandDialect == "couchdb"
}

func (d *Document) commandText(mongo, redis, elasticsearch, easysearch, solr, couchdb string) string {
	if d.commandDialect == "couchdb" {
		return locales.Text(couchdb)
	}
	if d.commandDialect == "solr" {
		return locales.Text(solr)
	}
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
