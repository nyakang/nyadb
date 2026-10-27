package shell

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// researchIconResources is an explicit allowlist of Qt resource filenames
// verified in the original ELF. File names never come from user input.
var researchIconResources = map[string]string{
	"MySQL":                "OV_64_MySQL_On.png",
	"PostgreSQL":           "OV_64_PostgreSQL_On.png",
	"SQL Server":           "OV_64_SQLserver_On.png",
	"Oracle":               "OV_64_Oracle_On.png",
	"SQLite":               "OV_64_SQLite_On.png",
	"MariaDB":              "OV_64_MariaDB_On.png",
	"MongoDB":              "OV_64_MongoDB_On.png",
	"Redis":                "OV_64_Redis_On.png",
	"Snowflake":            "OV_64_Snowflake_On.png",
	"达梦":                   "OV_64_Dameng_On.png",
	"OceanBase":            "OV_64_OceanBase_On.png",
	"阿里云 RDS MySQL 版":      "OV_64_Alibaba_Cloud_MySQL_On.png",
	"阿里云 RDS PostgreSQL 版": "OV_64_Alibaba_Cloud_PostgreSQL_On.png",
	"阿里云 RDS SQL Server 版": "OV_64_Alibaba_Cloud_SQLsever_On.png",
	"阿里云 MongoDB 版":        "OV_64_Alibaba_Cloud_MongoDB_On.png",
	"阿里云 Redis 版":          "OV_64_Alibaba_Cloud_Redis_On.png",
	"腾讯云 MySQL":            "OV_64_Tencent_Cloud_MySQL_On.png",
	"华为云 PostgreSQL":       "OV_64_Huawei_Cloud_PostgreSQL_On.png",
	"AWS RDS MySQL":        "OV_64_AmazonRDS_MySQL_On.png",
	"Azure SQL":            "OV_64_Microsoft_Azure_SQLserver_On.png",
}

const researchIconMaxBytes = 1024 * 1024

// LoadLocalResearchIcons reads optional, locally extracted PNGs once, not in
// the frame renderer. Missing assets fall back to NyaDB-authored SVGs.
//
// This is ONLY used when bootstrap explicitly enables local research mode.
// No Navicat image is embedded, copied to resources, or shipped in builds.
func (s *State) LoadLocalResearchIcons(dir string) (int, error) {
	s.researchIcons = nil
	if dir == "" {
		return 0, nil
	}
	icons := make(map[string]*ui.Bitmap)
	var problems []error
	for engine, filename := range researchIconResources {
		path := filepath.Join(dir, filename)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", filename, err))
			continue
		}
		if !info.Mode().IsRegular() || info.Size() > researchIconMaxBytes {
			problems = append(problems, fmt.Errorf("%s: not a regular PNG <= 1 MiB", filename))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", filename, err))
			continue
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != "png" || cfg.Width != 64 || cfg.Height != 64 {
			problems = append(problems, fmt.Errorf("%s: expected a valid 64x64 PNG", filename))
			continue
		}
		bitmap, err := ui.DecodeBitmap(data)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: decode: %w", filename, err))
			continue
		}
		icons[engine] = bitmap
	}
	s.researchIcons = icons
	return len(icons), errors.Join(problems...)
}

func (s *State) catalogIcon(c *ui.Context, item catalogEntry, size float32) {
	palette := theme.Colors(c)

	if bitmap := s.researchIcons[item.name]; bitmap != nil {
		ui.Image(c, bitmap).Size(size*1.35, size*1.35).Fit(ui.Contain)
		return
	}
	if bitmap := s.bundledIcons[item.name]; bitmap != nil {
		ui.Image(c, bitmap).Size(size*1.16, size*1.16).Fit(ui.Contain)
		return
	}
	ui.Box(c).Size(size, size).Radius(6).Background(ui.Hex(item.color)).Center().Children(func() {
		symbolSize := size * 0.64
		theme.Icon(c, item.symbol, symbolSize, palette.Text)
	})
}
