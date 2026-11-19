package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jeffk/kicad-lib/internal/kicad"
	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database handle and provides CRUD and FTS5 operations.
type DB struct {
	conn *sql.DB
}

// Open opens (or creates) the SQLite database at dbPath and runs schema migrations.
func Open(dbPath string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1)

	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	d := &DB{conn: conn}
	if err := d.migrate(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return d, nil
}

// Close closes the underlying SQLite connection.
func (d *DB) Close() error {
	if d == nil || d.conn == nil {
		return nil
	}
	return d.conn.Close()
}

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS libraries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS parts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uuid TEXT NOT NULL UNIQUE,
			library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			value TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			keywords TEXT NOT NULL DEFAULT '',
			datasheet TEXT NOT NULL DEFAULT '',
			mpn TEXT NOT NULL DEFAULT '',
			manufacturer TEXT NOT NULL DEFAULT '',
			lcsc_part TEXT NOT NULL DEFAULT '',
			source_type TEXT NOT NULL DEFAULT 'manual',
			source_ref TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(library_id, name)
		);`,
		`CREATE TABLE IF NOT EXISTS symbols (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			part_id INTEGER NOT NULL UNIQUE REFERENCES parts(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			raw_sexpr TEXT NOT NULL DEFAULT '',
			vault_path TEXT NOT NULL DEFAULT '',
			pin_count INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS footprints (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			part_id INTEGER NOT NULL UNIQUE REFERENCES parts(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			raw_sexpr TEXT NOT NULL DEFAULT '',
			vault_path TEXT NOT NULL DEFAULT '',
			pad_count INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS models_3d (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			part_id INTEGER NOT NULL REFERENCES parts(id) ON DELETE CASCADE,
			footprint_id INTEGER NOT NULL DEFAULT 0,
			file_name TEXT NOT NULL,
			format TEXT NOT NULL DEFAULT 'step',
			vault_path TEXT NOT NULL DEFAULT '',
			offset_x REAL NOT NULL DEFAULT 0,
			offset_y REAL NOT NULL DEFAULT 0,
			offset_z REAL NOT NULL DEFAULT 0,
			scale_x REAL NOT NULL DEFAULT 1,
			scale_y REAL NOT NULL DEFAULT 1,
			scale_z REAL NOT NULL DEFAULT 1,
			rotate_x REAL NOT NULL DEFAULT 0,
			rotate_y REAL NOT NULL DEFAULT 0,
			rotate_z REAL NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS part_metadata (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			part_id INTEGER NOT NULL REFERENCES parts(id) ON DELETE CASCADE,
			meta_key TEXT NOT NULL,
			meta_value TEXT NOT NULL DEFAULT '',
			UNIQUE(part_id, meta_key)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_parts_library ON parts(library_id);`,
		`CREATE INDEX IF NOT EXISTS idx_metadata_kv ON part_metadata(meta_key, meta_value);`,
		`CREATE INDEX IF NOT EXISTS idx_metadata_part ON part_metadata(part_id);`,
		`CREATE INDEX IF NOT EXISTS idx_models3d_part ON models_3d(part_id);`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS parts_fts USING fts5(
			part_id UNINDEXED,
			name,
			value,
			description,
			keywords,
			mpn,
			manufacturer,
			lcsc_part,
			metadata_text
		);`,
	}

	for _, stmt := range stmts {
		if _, err := d.conn.Exec(stmt); err != nil {
			return fmt.Errorf("exec migration %q: %w", stmt[:min(48, len(stmt))], err)
		}
	}

	// Idempotent column migrations on libraries table
	cols, err := d.tableColumns("libraries")
	if err != nil {
		return err
	}
	alterStmts := []struct {
		col string
		sql string
	}{
		{"source_uri", `ALTER TABLE libraries ADD COLUMN source_uri TEXT NOT NULL DEFAULT ''`},
		{"fp_source_uri", `ALTER TABLE libraries ADD COLUMN fp_source_uri TEXT NOT NULL DEFAULT ''`},
		{"source_mtime", `ALTER TABLE libraries ADD COLUMN source_mtime INTEGER NOT NULL DEFAULT 0`},
	}
	for _, a := range alterStmts {
		if !cols[a.col] {
			if _, err := d.conn.Exec(a.sql); err != nil {
				return fmt.Errorf("alter libraries add %s: %w", a.col, err)
			}
		}
	}
	return nil
}

func (d *DB) tableColumns(table string) (map[string]bool, error) {
	rows, err := d.conn.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// CreateLibrary creates a new library row.
func (d *DB) CreateLibrary(name, description string) (*Library, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("library name cannot be empty")
	}
	now := time.Now().UTC()
	res, err := d.conn.Exec(
		`INSERT INTO libraries (name, description, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		name, description, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create library %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetLibrary(id)
}

// GetLibrary fetches a library by ID.
func (d *DB) GetLibrary(id int64) (*Library, error) {
	row := d.conn.QueryRow(`
		SELECT l.id, l.name, l.description, l.source_uri, l.fp_source_uri, l.source_mtime, l.created_at, l.updated_at, COUNT(p.id)
		FROM libraries l
		LEFT JOIN parts p ON p.library_id = l.id
		WHERE l.id = ?
		GROUP BY l.id`, id)
	var lib Library
	if err := row.Scan(&lib.ID, &lib.Name, &lib.Description, &lib.SourceURI, &lib.FPSourceURI, &lib.SourceMtime, &lib.CreatedAt, &lib.UpdatedAt, &lib.PartCount); err != nil {
		return nil, err
	}
	return &lib, nil
}

// GetLibraryByName fetches a library by its exact name.
func (d *DB) GetLibraryByName(name string) (*Library, error) {
	row := d.conn.QueryRow(`
		SELECT l.id, l.name, l.description, l.source_uri, l.fp_source_uri, l.source_mtime, l.created_at, l.updated_at, COUNT(p.id)
		FROM libraries l
		LEFT JOIN parts p ON p.library_id = l.id
		WHERE l.name = ?
		GROUP BY l.id`, strings.TrimSpace(name))
	var lib Library
	if err := row.Scan(&lib.ID, &lib.Name, &lib.Description, &lib.SourceURI, &lib.FPSourceURI, &lib.SourceMtime, &lib.CreatedAt, &lib.UpdatedAt, &lib.PartCount); err != nil {
		return nil, err
	}
	return &lib, nil
}

// ListLibraries returns all libraries ordered alphabetically with part counts.
func (d *DB) ListLibraries() ([]Library, error) {
	rows, err := d.conn.Query(`
		SELECT l.id, l.name, l.description, l.source_uri, l.fp_source_uri, l.source_mtime, l.created_at, l.updated_at, COUNT(p.id)
		FROM libraries l
		LEFT JOIN parts p ON p.library_id = l.id
		GROUP BY l.id
		ORDER BY LOWER(l.name) ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var libs []Library
	for rows.Next() {
		var lib Library
		if err := rows.Scan(&lib.ID, &lib.Name, &lib.Description, &lib.SourceURI, &lib.FPSourceURI, &lib.SourceMtime, &lib.CreatedAt, &lib.UpdatedAt, &lib.PartCount); err != nil {
			return nil, err
		}
		libs = append(libs, lib)
	}
	return libs, rows.Err()
}

// IsLibrarySyncUpToDate reports whether a library already exists with matching sourceURI and sourceMtime.
func (d *DB) IsLibrarySyncUpToDate(name, sourceURI string, sourceMtime int64) (bool, error) {
	if sourceMtime <= 0 {
		return false, nil
	}
	lib, err := d.GetLibraryByName(name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	matchesURI := lib.SourceURI == sourceURI || (sourceURI != "" && lib.SourceURI == "" && lib.FPSourceURI == sourceURI)
	return matchesURI && lib.SourceMtime == sourceMtime, nil
}

// BulkSyncLibrary syncs an external KiCad library into SQLite in a single transaction,
// preserving any user-added or modified parts (source_type != 'kicad_sync').
func (d *DB) BulkSyncLibrary(name, description, sourceURI, fpSourceURI string, sourceMtime int64, parts []Part) (*Library, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, errors.New("library name cannot be empty")
	}

	if existing, err := d.GetLibraryByName(name); err == nil {
		matchesURI := (existing.SourceURI == sourceURI) || (sourceURI == "" && existing.FPSourceURI == fpSourceURI)
		if matchesURI && existing.SourceMtime == sourceMtime && sourceMtime > 0 {
			return existing, false, nil
		}
	}

	now := time.Now().UTC()
	tx, err := d.conn.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var libID int64
	err = tx.QueryRow(`SELECT id FROM libraries WHERE name = ?`, name).Scan(&libID)
	if errors.Is(err, sql.ErrNoRows) {
		res, ierr := tx.Exec(
			`INSERT INTO libraries (name, description, source_uri, fp_source_uri, source_mtime, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			name, description, sourceURI, fpSourceURI, sourceMtime, now, now,
		)
		if ierr != nil {
			return nil, false, fmt.Errorf("insert library %q: %w", name, ierr)
		}
		libID, err = res.LastInsertId()
		if err != nil {
			return nil, false, err
		}
	} else if err != nil {
		return nil, false, err
	} else {
		if _, uerr := tx.Exec(
			`UPDATE libraries SET description = COALESCE(NULLIF(?, ''), description), source_uri = ?, fp_source_uri = ?, source_mtime = ?, updated_at = ? WHERE id = ?`,
			description, sourceURI, fpSourceURI, sourceMtime, now, libID,
		); uerr != nil {
			return nil, false, uerr
		}
	}

	// Remove previous kicad_sync parts while keeping manual/drop/git/lcsc parts intact
	if _, err := tx.Exec(`DELETE FROM parts_fts WHERE part_id IN (SELECT id FROM parts WHERE library_id = ? AND source_type = 'kicad_sync')`, libID); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`DELETE FROM parts WHERE library_id = ? AND source_type = 'kicad_sync'`, libID); err != nil {
		return nil, false, err
	}

	// Collect existing non-synced part names to avoid UNIQUE(library_id, name) collisions
	preservedNames := make(map[string]bool)
	pRows, err := tx.Query(`SELECT name FROM parts WHERE library_id = ?`, libID)
	if err != nil {
		return nil, false, err
	}
	for pRows.Next() {
		var pn string
		if pRows.Scan(&pn) == nil {
			preservedNames[pn] = true
		}
	}
	_ = pRows.Close()

	stmtPart, err := tx.Prepare(`
		INSERT INTO parts (uuid, library_id, name, value, description, keywords, datasheet, mpn, manufacturer, lcsc_part, source_type, source_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, false, err
	}
	defer stmtPart.Close()

	stmtSym, err := tx.Prepare(`INSERT INTO symbols (part_id, name, raw_sexpr, vault_path, pin_count) VALUES (?, ?, '', ?, ?)`)
	if err != nil {
		return nil, false, err
	}
	defer stmtSym.Close()

	stmtFP, err := tx.Prepare(`INSERT INTO footprints (part_id, name, raw_sexpr, vault_path, pad_count) VALUES (?, ?, '', ?, ?)`)
	if err != nil {
		return nil, false, err
	}
	defer stmtFP.Close()

	stmtM3D, err := tx.Prepare(`
		INSERT INTO models_3d (part_id, footprint_id, file_name, format, vault_path, offset_x, offset_y, offset_z, scale_x, scale_y, scale_z, rotate_x, rotate_y, rotate_z)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, false, err
	}
	defer stmtM3D.Close()

	stmtMeta, err := tx.Prepare(`INSERT INTO part_metadata (part_id, meta_key, meta_value) VALUES (?, ?, ?)`)
	if err != nil {
		return nil, false, err
	}
	defer stmtMeta.Close()

	stmtFTS, err := tx.Prepare(`
		INSERT INTO parts_fts (part_id, name, value, description, keywords, mpn, manufacturer, lcsc_part, metadata_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, false, err
	}
	defer stmtFTS.Close()

	seenInBatch := make(map[string]bool, len(parts))
	for i := range parts {
		p := &parts[i]
		pName := strings.TrimSpace(p.Name)
		if pName == "" || preservedNames[pName] || seenInBatch[pName] {
			continue
		}
		seenInBatch[pName] = true

		partUUID := p.UUID
		if partUUID == "" {
			partUUID = uuid.NewString()
		}
		srcType := p.SourceType
		if srcType == "" {
			srcType = "kicad_sync"
		}
		res, err := stmtPart.Exec(
			partUUID, libID, pName, p.Value, p.Description, p.Keywords, p.Datasheet,
			p.MPN, p.Manufacturer, p.LCSCPart, srcType, p.SourceRef, now, now,
		)
		if err != nil {
			return nil, false, fmt.Errorf("bulk insert part %q: %w", pName, err)
		}
		partID, err := res.LastInsertId()
		if err != nil {
			return nil, false, err
		}

		if p.Symbol != nil {
			if _, err := stmtSym.Exec(partID, p.Symbol.Name, p.Symbol.VaultPath, p.Symbol.PinCount); err != nil {
				return nil, false, err
			}
		}

		var fpID int64
		if p.Footprint != nil {
			fpRes, err := stmtFP.Exec(partID, p.Footprint.Name, p.Footprint.VaultPath, p.Footprint.PadCount)
			if err != nil {
				return nil, false, err
			}
			fpID, _ = fpRes.LastInsertId()
		}

		for _, m := range p.Models3D {
			sx, sy, sz := m.ScaleX, m.ScaleY, m.ScaleZ
			if sx == 0 && sy == 0 && sz == 0 {
				sx, sy, sz = 1, 1, 1
			}
			if _, err := stmtM3D.Exec(
				partID, fpID, m.FileName, m.Format, m.VaultPath,
				m.OffsetX, m.OffsetY, m.OffsetZ, sx, sy, sz, m.RotateX, m.RotateY, m.RotateZ,
			); err != nil {
				return nil, false, err
			}
		}

		for k, v := range p.Metadata {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, err := stmtMeta.Exec(partID, k, v); err != nil {
				return nil, false, err
			}
		}

		metaText := buildMetadataText(p.Metadata)
		if _, err := stmtFTS.Exec(partID, pName, p.Value, p.Description, p.Keywords, p.MPN, p.Manufacturer, p.LCSCPart, metaText); err != nil {
			return nil, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	lib, err := d.GetLibrary(libID)
	return lib, true, err
}

// DeleteLibrary removes a library and cascades to its parts.
func (d *DB) DeleteLibrary(id int64) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Remove FTS entries for parts in this library before cascading delete.
	if _, err := tx.Exec(`DELETE FROM parts_fts WHERE part_id IN (SELECT id FROM parts WHERE library_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM libraries WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SavePart inserts or updates a part along with its symbol, footprint, 3D models, metadata, and FTS index.
func (d *DB) SavePart(part *Part) error {
	if part == nil {
		return errors.New("part is nil")
	}
	part.Name = strings.TrimSpace(part.Name)
	if part.Name == "" {
		return errors.New("part name cannot be empty")
	}
	if part.LibraryID <= 0 {
		return errors.New("valid library_id is required")
	}
	if part.UUID == "" {
		part.UUID = uuid.NewString()
	}
	if part.SourceType == "" {
		part.SourceType = "manual"
	}

	now := time.Now().UTC()
	if part.CreatedAt.IsZero() {
		part.CreatedAt = now
	}
	part.UpdatedAt = now

	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if part.ID == 0 {
		res, err := tx.Exec(`
			INSERT INTO parts (uuid, library_id, name, value, description, keywords, datasheet, mpn, manufacturer, lcsc_part, source_type, source_ref, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			part.UUID, part.LibraryID, part.Name, part.Value, part.Description, part.Keywords,
			part.Datasheet, part.MPN, part.Manufacturer, part.LCSCPart, part.SourceType, part.SourceRef,
			part.CreatedAt, part.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert part %q: %w", part.Name, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		part.ID = id
	} else {
		if _, err := tx.Exec(`
			UPDATE parts
			SET library_id=?, name=?, value=?, description=?, keywords=?, datasheet=?, mpn=?, manufacturer=?, lcsc_part=?, source_type=?, source_ref=?, updated_at=?
			WHERE id=?`,
			part.LibraryID, part.Name, part.Value, part.Description, part.Keywords,
			part.Datasheet, part.MPN, part.Manufacturer, part.LCSCPart, part.SourceType, part.SourceRef,
			part.UpdatedAt, part.ID,
		); err != nil {
			return fmt.Errorf("update part %d: %w", part.ID, err)
		}
	}

	// Replace symbol
	if _, err := tx.Exec(`DELETE FROM symbols WHERE part_id = ?`, part.ID); err != nil {
		return err
	}
	if part.Symbol != nil {
		part.Symbol.PartID = part.ID
		res, err := tx.Exec(`
			INSERT INTO symbols (part_id, name, raw_sexpr, vault_path, pin_count)
			VALUES (?, ?, ?, ?, ?)`,
			part.ID, part.Symbol.Name, part.Symbol.RawSExpr, part.Symbol.VaultPath, part.Symbol.PinCount,
		)
		if err != nil {
			return fmt.Errorf("insert symbol: %w", err)
		}
		part.Symbol.ID, _ = res.LastInsertId()
	}

	// Replace footprint
	if _, err := tx.Exec(`DELETE FROM footprints WHERE part_id = ?`, part.ID); err != nil {
		return err
	}
	var fpID int64
	if part.Footprint != nil {
		part.Footprint.PartID = part.ID
		res, err := tx.Exec(`
			INSERT INTO footprints (part_id, name, raw_sexpr, vault_path, pad_count)
			VALUES (?, ?, ?, ?, ?)`,
			part.ID, part.Footprint.Name, part.Footprint.RawSExpr, part.Footprint.VaultPath, part.Footprint.PadCount,
		)
		if err != nil {
			return fmt.Errorf("insert footprint: %w", err)
		}
		fpID, _ = res.LastInsertId()
		part.Footprint.ID = fpID
	}

	// Replace 3D models
	if _, err := tx.Exec(`DELETE FROM models_3d WHERE part_id = ?`, part.ID); err != nil {
		return err
	}
	for i := range part.Models3D {
		m := &part.Models3D[i]
		m.PartID = part.ID
		if m.FootprintID == 0 {
			m.FootprintID = fpID
		}
		if m.ScaleX == 0 && m.ScaleY == 0 && m.ScaleZ == 0 {
			m.ScaleX, m.ScaleY, m.ScaleZ = 1, 1, 1
		}
		res, err := tx.Exec(`
			INSERT INTO models_3d (part_id, footprint_id, file_name, format, vault_path, offset_x, offset_y, offset_z, scale_x, scale_y, scale_z, rotate_x, rotate_y, rotate_z)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.PartID, m.FootprintID, m.FileName, m.Format, m.VaultPath,
			m.OffsetX, m.OffsetY, m.OffsetZ, m.ScaleX, m.ScaleY, m.ScaleZ, m.RotateX, m.RotateY, m.RotateZ,
		)
		if err != nil {
			return fmt.Errorf("insert model_3d %q: %w", m.FileName, err)
		}
		m.ID, _ = res.LastInsertId()
	}

	// Replace metadata
	if _, err := tx.Exec(`DELETE FROM part_metadata WHERE part_id = ?`, part.ID); err != nil {
		return err
	}
	for k, v := range part.Metadata {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO part_metadata (part_id, meta_key, meta_value)
			VALUES (?, ?, ?)`, kToPartID(part.ID), k, v); err != nil {
			return fmt.Errorf("insert metadata %q: %w", k, err)
		}
	}

	// Sync FTS5 row
	if err := syncPartFTSTx(tx, part); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE libraries SET updated_at = ? WHERE id = ?`, now, part.LibraryID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Populate LibraryName if missing
	if part.LibraryName == "" {
		if lib, err := d.GetLibrary(part.LibraryID); err == nil {
			part.LibraryName = lib.Name
		}
	}
	return nil
}

func kToPartID(id int64) int64 { return id }

func syncPartFTSTx(tx *sql.Tx, part *Part) error {
	if _, err := tx.Exec(`DELETE FROM parts_fts WHERE part_id = ?`, part.ID); err != nil {
		return err
	}
	metaText := buildMetadataText(part.Metadata)
	_, err := tx.Exec(`
		INSERT INTO parts_fts (part_id, name, value, description, keywords, mpn, manufacturer, lcsc_part, metadata_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		part.ID, part.Name, part.Value, part.Description, part.Keywords, part.MPN, part.Manufacturer, part.LCSCPart, metaText,
	)
	return err
}

func buildMetadataText(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		v := meta[k]
		parts = append(parts, k, v)
	}
	return strings.Join(parts, " ")
}

// GetPart loads a complete Part with its symbol, footprint, 3D models, and metadata.
func (d *DB) GetPart(id int64) (*Part, error) {
	row := d.conn.QueryRow(`
		SELECT p.id, p.uuid, p.library_id, l.name, p.name, p.value, p.description, p.keywords,
		       p.datasheet, p.mpn, p.manufacturer, p.lcsc_part, p.source_type, p.source_ref,
		       p.created_at, p.updated_at
		FROM parts p
		JOIN libraries l ON l.id = p.library_id
		WHERE p.id = ?`, id)

	var p Part
	err := row.Scan(
		&p.ID, &p.UUID, &p.LibraryID, &p.LibraryName, &p.Name, &p.Value, &p.Description, &p.Keywords,
		&p.Datasheet, &p.MPN, &p.Manufacturer, &p.LCSCPart, &p.SourceType, &p.SourceRef,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if err := d.hydratePartRelations(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (d *DB) hydratePartRelations(p *Part) error {
	// Symbol
	var sym Symbol
	err := d.conn.QueryRow(`SELECT id, part_id, name, raw_sexpr, vault_path, pin_count FROM symbols WHERE part_id = ?`, p.ID).
		Scan(&sym.ID, &sym.PartID, &sym.Name, &sym.RawSExpr, &sym.VaultPath, &sym.PinCount)
	if err == nil {
		if sym.RawSExpr == "" && sym.VaultPath != "" {
			if raw, e := kicad.ExtractSymbolSExprFromFile(sym.VaultPath, sym.Name); e == nil {
				sym.RawSExpr = raw
			}
		}
		p.Symbol = &sym
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// Footprint
	var fp Footprint
	err = d.conn.QueryRow(`SELECT id, part_id, name, raw_sexpr, vault_path, pad_count FROM footprints WHERE part_id = ?`, p.ID).
		Scan(&fp.ID, &fp.PartID, &fp.Name, &fp.RawSExpr, &fp.VaultPath, &fp.PadCount)
	if err == nil {
		if fp.RawSExpr == "" && fp.VaultPath != "" {
			if b, e := os.ReadFile(fp.VaultPath); e == nil {
				fp.RawSExpr = string(b)
			}
		}
		p.Footprint = &fp
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// 3D models
	mRows, err := d.conn.Query(`
		SELECT id, part_id, footprint_id, file_name, format, vault_path,
		       offset_x, offset_y, offset_z, scale_x, scale_y, scale_z, rotate_x, rotate_y, rotate_z
		FROM models_3d WHERE part_id = ? ORDER BY id ASC`, p.ID)
	if err != nil {
		return err
	}
	defer mRows.Close()
	for mRows.Next() {
		var m Model3D
		if err := mRows.Scan(
			&m.ID, &m.PartID, &m.FootprintID, &m.FileName, &m.Format, &m.VaultPath,
			&m.OffsetX, &m.OffsetY, &m.OffsetZ, &m.ScaleX, &m.ScaleY, &m.ScaleZ, &m.RotateX, &m.RotateY, &m.RotateZ,
		); err != nil {
			return err
		}
		p.Models3D = append(p.Models3D, m)
	}
	if err := mRows.Err(); err != nil {
		return err
	}

	// Metadata
	p.Metadata = make(map[string]string)
	kRows, err := d.conn.Query(`SELECT meta_key, meta_value FROM part_metadata WHERE part_id = ? ORDER BY meta_key ASC`, p.ID)
	if err != nil {
		return err
	}
	defer kRows.Close()
	for kRows.Next() {
		var k, v string
		if err := kRows.Scan(&k, &v); err != nil {
			return err
		}
		p.Metadata[k] = v
	}
	return kRows.Err()
}

// ListPartsByLibrary lists all parts belonging to a given library (or all libraries when libraryID <= 0)
// with full relation and lazy RawSExpr hydration for library regeneration/export.
func (d *DB) ListPartsByLibrary(libraryID int64) ([]Part, error) {
	query := `
		SELECT p.id, p.uuid, p.library_id, l.name, p.name, p.value, p.description, p.keywords,
		       p.datasheet, p.mpn, p.manufacturer, p.lcsc_part, p.source_type, p.source_ref,
		       p.created_at, p.updated_at
		FROM parts p
		JOIN libraries l ON l.id = p.library_id`
	var args []any
	if libraryID > 0 {
		query += ` WHERE p.library_id = ?`
		args = append(args, libraryID)
	}
	query += ` ORDER BY LOWER(p.name) ASC`

	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var parts []Part
	for rows.Next() {
		var p Part
		if err := rows.Scan(
			&p.ID, &p.UUID, &p.LibraryID, &p.LibraryName, &p.Name, &p.Value, &p.Description, &p.Keywords,
			&p.Datasheet, &p.MPN, &p.Manufacturer, &p.LCSCPart, &p.SourceType, &p.SourceRef,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range parts {
		if err := d.hydratePartRelations(&parts[i]); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// MovePart moves a part to a different library.
func (d *DB) MovePart(partID, targetLibraryID int64) error {
	now := time.Now().UTC()
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldLibID int64
	if err := tx.QueryRow(`SELECT library_id FROM parts WHERE id = ?`, partID).Scan(&oldLibID); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE parts SET library_id = ?, source_type = CASE WHEN source_type = 'kicad_sync' THEN 'manual' ELSE source_type END, updated_at = ? WHERE id = ?`, targetLibraryID, now, partID); err != nil {
		return fmt.Errorf("move part %d to library %d: %w", partID, targetLibraryID, err)
	}
	if _, err := tx.Exec(`UPDATE libraries SET updated_at = ? WHERE id IN (?, ?)`, now, oldLibID, targetLibraryID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdatePartMetadata replaces custom metadata on a part and updates the FTS5 index.
func (d *DB) UpdatePartMetadata(partID int64, metadata map[string]string) error {
	part, err := d.GetPart(partID)
	if err != nil {
		return err
	}
	part.Metadata = metadata
	return d.SavePart(part)
}

// DeletePart deletes a part and removes it from FTS5.
func (d *DB) DeletePart(partID int64) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM parts_fts WHERE part_id = ?`, partID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM parts WHERE id = ?`, partID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListDistinctMetadataKeys returns all metadata key names currently present across parts.
func (d *DB) ListDistinctMetadataKeys() ([]string, error) {
	rows, err := d.conn.Query(`SELECT DISTINCT meta_key FROM part_metadata ORDER BY LOWER(meta_key) ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// FindDuplicateParts searches for existing parts matching by case-insensitive Name,
// non-empty MPN, or non-empty LCSC Part ID. Matches in targetLibraryID are ordered first.
func (d *DB) FindDuplicateParts(name, mpn, lcscPart string, targetLibraryID int64) ([]Part, error) {
	name = strings.TrimSpace(name)
	mpn = strings.TrimSpace(mpn)
	lcscPart = strings.TrimSpace(lcscPart)
	if name == "" && mpn == "" && lcscPart == "" {
		return nil, nil
	}

	rows, err := d.conn.Query(`
		SELECT p.id, p.uuid, p.library_id, l.name, p.name, p.value, p.description, p.keywords,
		       p.datasheet, p.mpn, p.manufacturer, p.lcsc_part, p.source_type, p.source_ref,
		       p.created_at, p.updated_at
		FROM parts p
		JOIN libraries l ON l.id = p.library_id
		WHERE (? != '' AND LOWER(p.name) = LOWER(?))
		   OR (? != '' AND p.mpn != '' AND LOWER(p.mpn) = LOWER(?))
		   OR (? != '' AND p.lcsc_part != '' AND LOWER(p.lcsc_part) = LOWER(?))
		ORDER BY CASE WHEN p.library_id = ? THEN 0 ELSE 1 END, LOWER(p.name) ASC
		LIMIT 20`,
		name, name, mpn, mpn, lcscPart, lcscPart, targetLibraryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Part
	for rows.Next() {
		var p Part
		if err := rows.Scan(
			&p.ID, &p.UUID, &p.LibraryID, &p.LibraryName, &p.Name, &p.Value, &p.Description, &p.Keywords,
			&p.Datasheet, &p.MPN, &p.Manufacturer, &p.LCSCPart, &p.SourceType, &p.SourceRef,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := d.hydratePartRelations(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
