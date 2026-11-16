package localdb

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func decodeDatabendSavedOptions(p *connections.Profile, raw string) error {
	var value engineOptionsEnvelope
	var fields map[string]json.RawMessage
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if raw == "" || len(raw) > 128<<10 || !uniqueNativeAuthOptionsJSON(raw) || d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF ||
		json.Unmarshal([]byte(raw), &fields) != nil || len(fields) != 3 || fields["version"] == nil || fields["kind"] == nil || fields["databend"] == nil ||
		value.Version != 1 || value.Kind != p.Kind || value.Databend == nil || value.Databend.Version != 1 {
		return connections.ErrInvalid
	}
	p.DatabendOptions = *value.Databend
	checked, err := connections.NormalizeProfile(*p)
	*p = checked
	return err
}
