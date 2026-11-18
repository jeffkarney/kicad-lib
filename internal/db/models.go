package db

import "time"

// Library represents a logical KiCad library managed by kicad-lib.
type Library struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SourceURI   string    `json:"source_uri"`
	FPSourceURI string    `json:"fp_source_uri"`
	SourceMtime int64     `json:"source_mtime"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	PartCount   int       `json:"part_count"`
}

// Part represents an electronic component tracked in the vault database.
type Part struct {
	ID           int64             `json:"id"`
	UUID         string            `json:"uuid"`
	LibraryID    int64             `json:"library_id"`
	LibraryName  string            `json:"library_name"`
	Name         string            `json:"name"`
	Value        string            `json:"value"`
	Description  string            `json:"description"`
	Keywords     string            `json:"keywords"`
	Datasheet    string            `json:"datasheet"`
	MPN          string            `json:"mpn"`
	Manufacturer string            `json:"manufacturer"`
	LCSCPart     string            `json:"lcsc_part"`
	SourceType   string            `json:"source_type"`
	SourceRef    string            `json:"source_ref"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Symbol       *Symbol           `json:"symbol,omitempty"`
	Footprint    *Footprint        `json:"footprint,omitempty"`
	Models3D     []Model3D         `json:"models_3d,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// Symbol stores schematic symbol metadata and canonical S-expression text.
type Symbol struct {
	ID        int64  `json:"id"`
	PartID    int64  `json:"part_id"`
	Name      string `json:"name"`
	RawSExpr  string `json:"raw_sexpr"`
	VaultPath string `json:"vault_path"`
	PinCount  int    `json:"pin_count"`
}

// Footprint stores PCB footprint metadata and canonical S-expression text.
type Footprint struct {
	ID        int64  `json:"id"`
	PartID    int64  `json:"part_id"`
	Name      string `json:"name"`
	RawSExpr  string `json:"raw_sexpr"`
	VaultPath string `json:"vault_path"`
	PadCount  int    `json:"pad_count"`
}

// Model3D stores a 3D STEP/WRL model associated with a part and footprint.
type Model3D struct {
	ID          int64   `json:"id"`
	PartID      int64   `json:"part_id"`
	FootprintID int64   `json:"footprint_id"`
	FileName    string  `json:"file_name"`
	Format      string  `json:"format"`
	VaultPath   string  `json:"vault_path"`
	OffsetX     float64 `json:"offset_x"`
	OffsetY     float64 `json:"offset_y"`
	OffsetZ     float64 `json:"offset_z"`
	ScaleX      float64 `json:"scale_x"`
	ScaleY      float64 `json:"scale_y"`
	ScaleZ      float64 `json:"scale_z"`
	RotateX     float64 `json:"rotate_x"`
	RotateY     float64 `json:"rotate_y"`
	RotateZ     float64 `json:"rotate_z"`
}

// SearchFilter defines parameters for querying parts via FTS5 and metadata filters.
type SearchFilter struct {
	Query           string
	LibraryID       int64
	MetadataFilters map[string]string
	Has3DModel      *bool
}
