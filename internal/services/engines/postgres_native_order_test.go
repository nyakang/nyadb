package engines

import "testing"

func TestPostgreSQLNativeOrderUsesServerCompilerTargetAndPreservesUnknown(t *testing.T) {
	for version, wanted := range map[string]string{
		"PostgreSQL 17.10 on x86_64-pc-linux-musl, compiled by gcc, 64-bit":          "little",
		"PostgreSQL 17.10 on aarch64-unknown-linux-gnu, compiled by gcc, 64-bit":     "little",
		"PostgreSQL 17.10 on powerpc64le-unknown-linux-gnu, compiled by gcc, 64-bit": "little",
		"PostgreSQL 17.10 on powerpc64-unknown-linux-gnu, compiled by gcc, 64-bit":   "big",
		"PostgreSQL 17.10 on s390x-unknown-linux-gnu, compiled by gcc, 64-bit":       "big",
		"PostgreSQL 17.10 on aarch64_be-unknown-linux-gnu, compiled by gcc, 64-bit":  "",
		"PostgreSQL 17.10 on unsupported-machine, compiled by gcc, 64-bit":           "",
		"PostgreSQL 17.10, 64-bit": "",
		"PostgreSQL 17.10 on ":     "",
	} {
		if got := postgresNativeByteOrder(version); got != wanted {
			t.Fatal("native byte order inferred from a different architecture", version, got, wanted)
		}
	}
}
