package sqlworkspace

func graphPropertyExtendedDefault(kind string) (string, bool) {
	text, ok := map[string]string{
		"date":           "1970-01-01",
		"local_time":     "00:00:00",
		"local_datetime": "1970-01-01T00:00:00",
		"time":           `{"value":"00:00:00Z","offset_seconds":0}`,
		"datetime":       `{"value":"1970-01-01T00:00:00Z","offset_seconds":0,"zone":"UTC"}`,
		"duration":       `{"months":0,"days":0,"seconds":0,"nanoseconds":0}`,
		"point2d":        `{"srid":7203,"x":0,"y":0}`,
		"point3d":        `{"srid":9157,"x":0,"y":0,"z":0}`,
	}[kind]
	return text, ok
}
