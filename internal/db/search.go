package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// SearchParts queries parts using FTS5 full-text matching, library filtering,
// custom metadata key-value filters, and 3D model presence filters.
// It uses a single joined query without loading raw S-expressions for fast UI browsing.
func (d *DB) SearchParts(filter SearchFilter) ([]Part, error) {
	var args []any
	var where []string

	joinFTS := false
	ftsExpr := buildFTSQuery(filter.Query)
	if ftsExpr != "" {
		joinFTS = true
		where = append(where, "parts_fts MATCH ?")
		args = append(args, ftsExpr)
	}

	if filter.LibraryID > 0 {
		where = append(where, "p.library_id = ?")
		args = append(args, filter.LibraryID)
	}

	if filter.Has3DModel != nil {
		if *filter.Has3DModel {
			where = append(where, "EXISTS (SELECT 1 FROM models_3d m WHERE m.part_id = p.id)")
		} else {
			where = append(where, "NOT EXISTS (SELECT 1 FROM models_3d m WHERE m.part_id = p.id)")
		}
	}

	for k, v := range filter.MetadataFilters {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" && v == "" {
			continue
		}
		if k != "" && v != "" {
			where = append(where, `EXISTS (
				SELECT 1 FROM part_metadata pm
				WHERE pm.part_id = p.id
				  AND LOWER(pm.meta_key) = LOWER(?)
				  AND LOWER(pm.meta_value) LIKE LOWER(?)
			)`)
			args = append(args, k, "%"+v+"%")
		} else if k != "" {
			where = append(where, `EXISTS (
				SELECT 1 FROM part_metadata pm
				WHERE pm.part_id = p.id AND LOWER(pm.meta_key) = LOWER(?)
			)`)
			args = append(args, k)
		} else {
			where = append(where, `EXISTS (
				SELECT 1 FROM part_metadata pm
				WHERE pm.part_id = p.id AND LOWER(pm.meta_value) LIKE LOWER(?)
			)`)
			args = append(args, "%"+v+"%")
		}
	}

	var sb strings.Builder
	sb.WriteString(`
		SELECT p.id, p.uuid, p.library_id, l.name, p.name, p.value, p.description, p.keywords,
		       p.datasheet, p.mpn, p.manufacturer, p.lcsc_part, p.source_type, p.source_ref,
		       p.created_at, p.updated_at,
		       s.id, s.name, s.vault_path, s.pin_count,
		       f.id, f.name, f.vault_path, f.pad_count
		FROM parts p
		JOIN libraries l ON l.id = p.library_id
		LEFT JOIN symbols s ON s.part_id = p.id
		LEFT JOIN footprints f ON f.part_id = p.id`)
	if joinFTS {
		sb.WriteString(` JOIN parts_fts ON parts_fts.part_id = p.id`)
	}
	if len(where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(where, " AND "))
	}
	if joinFTS {
		sb.WriteString(" ORDER BY parts_fts.rank, LOWER(p.name) ASC")
	} else {
		sb.WriteString(" ORDER BY LOWER(p.name) ASC")
	}

	if filter.LibraryID == 0 && ftsExpr == "" && len(filter.MetadataFilters) == 0 {
		sb.WriteString(" LIMIT 500")
	}

	rows, err := d.conn.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("search parts: %w", err)
	}
	defer rows.Close()

	var parts []Part
	var partIDs []any
	partIndex := make(map[int64]int)

	for rows.Next() {
		var p Part
		var symID, symPins sql.NullInt64
		var symName, symVault sql.NullString
		var fpID, fpPads sql.NullInt64
		var fpName, fpVault sql.NullString

		if err := rows.Scan(
			&p.ID, &p.UUID, &p.LibraryID, &p.LibraryName, &p.Name, &p.Value, &p.Description, &p.Keywords,
			&p.Datasheet, &p.MPN, &p.Manufacturer, &p.LCSCPart, &p.SourceType, &p.SourceRef,
			&p.CreatedAt, &p.UpdatedAt,
			&symID, &symName, &symVault, &symPins,
			&fpID, &fpName, &fpVault, &fpPads,
		); err != nil {
			return nil, err
		}
		if symID.Valid {
			p.Symbol = &Symbol{
				ID:        symID.Int64,
				PartID:    p.ID,
				Name:      symName.String,
				VaultPath: symVault.String,
				PinCount:  int(symPins.Int64),
			}
		}
		if fpID.Valid {
			p.Footprint = &Footprint{
				ID:        fpID.Int64,
				PartID:    p.ID,
				Name:      fpName.String,
				VaultPath: fpVault.String,
				PadCount:  int(fpPads.Int64),
			}
		}
		partIndex[p.ID] = len(parts)
		partIDs = append(partIDs, p.ID)
		parts = append(parts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Batch-load 3D model summaries in chunks
	const chunkSize = 400
	for start := 0; start < len(partIDs); start += chunkSize {
		end := min(start+chunkSize, len(partIDs))
		chunk := partIDs[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		mQuery := fmt.Sprintf(`
			SELECT id, part_id, footprint_id, file_name, format, vault_path,
			       offset_x, offset_y, offset_z, scale_x, scale_y, scale_z, rotate_x, rotate_y, rotate_z
			FROM models_3d WHERE part_id IN (%s) ORDER BY id ASC`, placeholders)
		mRows, err := d.conn.Query(mQuery, chunk...)
		if err != nil {
			return nil, err
		}
		for mRows.Next() {
			var m Model3D
			if err := mRows.Scan(
				&m.ID, &m.PartID, &m.FootprintID, &m.FileName, &m.Format, &m.VaultPath,
				&m.OffsetX, &m.OffsetY, &m.OffsetZ, &m.ScaleX, &m.ScaleY, &m.ScaleZ, &m.RotateX, &m.RotateY, &m.RotateZ,
			); err != nil {
				_ = mRows.Close()
				return nil, err
			}
			if idx, ok := partIndex[m.PartID]; ok {
				parts[idx].Models3D = append(parts[idx].Models3D, m)
			}
		}
		if err := mRows.Err(); err != nil {
			_ = mRows.Close()
			return nil, err
		}
		_ = mRows.Close()
	}

	return parts, nil
}

// buildFTSQuery turns a free-text query into safe FTS5 prefix terms so dashes, slashes,
// dots, or colons in part numbers don't trigger FTS5 syntax errors.
func buildFTSQuery(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return ""
	}
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		clean := strings.ReplaceAll(f, `"`, `""`)
		if clean == "" {
			continue
		}
		terms = append(terms, `"`+clean+`"*`)
	}
	return strings.Join(terms, " ")
}
