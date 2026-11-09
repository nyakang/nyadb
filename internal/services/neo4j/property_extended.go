package neo4j

import (
	"math/big"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func propertyExtended(v contract.GraphValue) (any, string, error) {
	value, err := contract.GraphPropertyExtendedValue(v)
	if err != nil {
		return nil, "", ErrInvalid
	}
	switch v.Kind {
	case "date":
		return nd.Date(value.(time.Time)), "DATE NOT NULL", nil
	case "local_time":
		return nd.LocalTime(value.(time.Time)), "LOCAL TIME NOT NULL", nil
	case "local_datetime":
		return nd.LocalDateTime(value.(time.Time)), "LOCAL DATETIME NOT NULL", nil
	case "time":
		return nd.Time(value.(time.Time)), "ZONED TIME NOT NULL", nil
	case "datetime":
		return value, "ZONED DATETIME NOT NULL", nil
	case "duration":
		d := value.(contract.GraphPropertyDurationValue)
		// Neo4j checks each average-length multiplication and addition for int64
		// overflow, even if a later component would cancel the overflow.
		months := new(big.Int).Mul(big.NewInt(d.Months), big.NewInt(2629746))
		days := new(big.Int).Mul(big.NewInt(d.Days), big.NewInt(86400))
		average := new(big.Int).Add(months, days)
		if !months.IsInt64() || !days.IsInt64() || !average.IsInt64() || !average.Add(average, big.NewInt(d.Seconds)).IsInt64() {
			return nil, "", ErrInvalid
		}
		return nd.Duration{Months: d.Months, Days: d.Days, Seconds: d.Seconds, Nanos: d.Nanoseconds}, "DURATION NOT NULL", nil
	case "point2d":
		p := value.(contract.GraphPropertyPointValue)
		return nd.Point2D{SpatialRefId: p.SRID, X: p.X, Y: p.Y}, "POINT NOT NULL", nil
	case "point3d":
		p := value.(contract.GraphPropertyPointValue)
		return nd.Point3D{SpatialRefId: p.SRID, X: p.X, Y: p.Y, Z: p.Z}, "POINT NOT NULL", nil
	}
	return nil, "", ErrInvalid
}
