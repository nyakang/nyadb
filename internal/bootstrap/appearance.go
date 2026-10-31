package bootstrap

import (
	"database/sql"
	"fmt"

	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func loadAppearance(db *sql.DB) error {
	mode, err := localdb.Preference(db, theme.ModePreferenceKey)
	if err != nil {
		return fmt.Errorf("read appearance preference: %w", err)
	}
	scheme, err := localdb.Preference(db, theme.SchemePreferenceKey)
	if err != nil {
		return fmt.Errorf("read color scheme preference: %w", err)
	}
	metrics, err := localdb.Preference(db, theme.MetricsPreferenceKey)
	if err != nil {
		return fmt.Errorf("read reading preferences: %w", err)
	}
	// Missing/unknown schemes keep the classic palette and the user's saved
	// light/dark mode. Both values are read before changing process state.
	theme.SetMode(theme.ParseMode(mode))
	theme.SetScheme(theme.ParseScheme(scheme))
	theme.SetMetrics(theme.DecodeMetrics(metrics))
	return nil
}
