# Navicat 18 connection type icon name ↔ PNG mapping (S)

Observed/verified 2026-10-21, inspected file:
`temp/navicat/squashfs-root/usr/bin/navicat` (Linux x86-64 edition).

**Finding:** The original ELF contains Qt RCC metadata connecting exact file
names such as `:/images/OV_64_MySQL_On.png` to embedded PNG byte ranges.
The mapping is **not merely guessed from the icon appearance**. We checked
the Qt name table (UTF-16BE), corresponding resource-tree entry, data index,
PNG length framing, chunk CRCs and image dimensions; screenshot appearance
provides additional independent visual corroboration.

These addresses are ELF **file offsets** in the inspected binary, not virtual
memory addresses or portable constants. Another Navicat version/build will
have different offsets.

## Exact mappings for the 64×64 connection chooser icons

The full Qt resource path for every entry is
`:/images/OV_64_<resource suffix>.png`, using `_On` and `_Off`:

| Database | Qt resource suffix | On PNG file offset | Off PNG file offset |
|---|---|---|---|
| MySQL | `MySQL` | `0x1dac4dc` | `0x1c4da28` |
| PostgreSQL | `PostgreSQL` | `0x2eba4fc` | `0x20db4e8` |
| SQL Server | `SQLserver` | `0x2bb0cd7` | `0x10d0db1` |
| Oracle | `Oracle` | `0x303fdad` | `0x1c415ee` |
| SQLite | `SQLite` | `0x2a5a33a` | `0x14ede29` |
| MariaDB | `MariaDB` | `0x3a11077` | `0x1e40b17` |
| MongoDB | `MongoDB` | `0x225252f` | `0xfb8d90` |
| Redis | `Redis` | `0x353d284` | `0x1677e41` |
| Snowflake | `Snowflake` | `0x1e65eea` | `0x2e0b6d0` |
| Dameng (达梦) | `Dameng` | `0x2aa7b75` | `0x2e04e19` |
| OceanBase | `OceanBase` | `0x39eaa58` | `0x2545258` |

Each of these 22 resource relationships was resolved via the Qt resource
name/tree/data structures and its PNG bytes passed validation.
`On` variants render colorful logos; `Off` variants are greyscale in
the inspected sample. Exact UI state-transition semantics remain Unknown.

## Additional cloud catalog resource mappings (S)

The following nine `On` resources have also been independently resolved
through the same Qt resource name/tree/data and PNG CRC validation:

| Catalog item | Exact resource filename | PNG offset |
|---|---|---|
| 阿里云 RDS MySQL 版 | `OV_64_Alibaba_Cloud_MySQL_On.png` | `0x1625b55` |
| 阿里云 RDS PostgreSQL 版 | `OV_64_Alibaba_Cloud_PostgreSQL_On.png` | `0x198670b` |
| 阿里云 RDS SQL Server 版 | `OV_64_Alibaba_Cloud_SQLsever_On.png` | `0x3b5bad7` |
| 阿里云 MongoDB 版 | `OV_64_Alibaba_Cloud_MongoDB_On.png` | `0x2a47e22` |
| 阿里云 Redis 版 | `OV_64_Alibaba_Cloud_Redis_On.png` | `0x12ee322` |
| 腾讯云 MySQL | `OV_64_Tencent_Cloud_MySQL_On.png` | `0x1f79723` |
| 华为云 PostgreSQL | `OV_64_Huawei_Cloud_PostgreSQL_On.png` | `0x1474179` |
| AWS RDS MySQL | `OV_64_AmazonRDS_MySQL_On.png` | `0x1e6f4a5` |
| Azure SQL | `OV_64_Microsoft_Azure_SQLserver_On.png` | `0x13c8d6e` |

The spelling `SQLsever` is present in the original Qt resource filename;
it has not been normalized or renamed. These are all validated 64×64 PNGs.

## Reproduce the study

```bash
python3 tools/research_navicat_png.py --input \
  temp/navicat/squashfs-root/usr/bin/navicat \
  temp/navicat/squashfs-root/usr/lib/libcc.so
python3 tools/research_navicat_qt_resources.py
```

The second script statically resolves known Qt resources with build-specific
name/data bases, checks unique valid resource references, writes research-only
`mapping.csv` and 22 named PNG samples under:

`temp/research/navicat-png-inventory/named/`

With Pillow installed, it also produces
`temp/research/navicat-png-inventory/named/resource-map-preview.png`,
containing the icon name, colored/grey states and exact offsets.

`tools/research_navicat_icon_sheet.py` builds broad contact sheets when
new image groups need manual discovery; it does not prove attribution.

## NyaDB product boundary

**No extracted Navicat icons were added to NyaDB**. The shipped native UI
continues using its independently drawn SVG symbols. A concrete mapping now
exists for faithful visual study, but proprietary Navicat image files should
not be copied into the public application's binary or committed under
`resources/` or `internal/`. Use appropriately licensed original database
brand marks or independently created equivalents for NyaDB instead.

Evidence tags: **S** = direct original ELF static evidence for the mapping;
**O** = original program screenshot confirms the visual appearance.
This does not establish source-code equivalence or constitute a differential
verification (**V**) of the full UI.
