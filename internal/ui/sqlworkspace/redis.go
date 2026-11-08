package sqlworkspace

import "github.com/nyakang/nyadb/locales"

func (d *Document) restRequests() bool {
	return d.commandDialect == "qdrant" || d.commandDialect == "elasticsearch" || d.commandDialect == "easysearch" || d.commandDialect == "solr" || d.commandDialect == "couchdb" || d.commandDialect == "meilisearch"
}

func (d *Document) commandText(mongo, redis, elasticsearch, easysearch, solr, couchdb, meilisearch, qdrant string) string {
	if d.commandDialect == "qdrant" {
		return locales.Text(qdrant)
	}
	if d.commandDialect == "meilisearch" {
		return locales.Text(meilisearch)
	}
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
