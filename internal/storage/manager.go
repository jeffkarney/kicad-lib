package storage

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jeffk/kicad-lib/internal/config"
	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/kicad"
)

// ImportModel3D describes a 3D model file staged for import into the vault.
type ImportModel3D struct {
	SourcePath string
	FileName   string
	Format     string
	OffsetX    float64
	OffsetY    float64
	OffsetZ    float64
	ScaleX     float64
	ScaleY     float64
	ScaleZ     float64
	RotateX    float64
	RotateY    float64
	RotateZ    float64
}

// ImportPartRequest holds all assets and metadata needed to import a part into a library.
type ImportPartRequest struct {
	LibraryID    int64
	PartName     string
	Value        string
	Description  string
	Keywords     string
	Datasheet    string
	MPN          string
	Manufacturer string
	LCSCPart     string
	SourceType   string
	SourceRef    string

	SymbolName       string
	SymbolSExpr      string
	SymbolSourcePath string

	FootprintName       string
	FootprintSExpr      string
	FootprintSourcePath string

	Models3D []ImportModel3D
	Metadata map[string]string
}

// SyncProgress reports live progress while pulling and syncing existing KiCad libraries.
type SyncProgress struct {
	Current     int
	Total       int
	LibraryName string
	PartsSynced int
}

// Manager coordinates vault asset storage, SQLite persistence, KiCad library generation,
// library table registration, and KiCad IPC notifications.
type Manager struct {
	cfg     *config.Config
	db      *db.DB
	ipc     *kicad.IPCClient
	syncMu  sync.Mutex
	syncing atomic.Bool
}

// NewManager creates a storage Manager.
func NewManager(cfg *config.Config, database *db.DB, ipc *kicad.IPCClient) (*Manager, error) {
	if err := cfg.EnsureDirs(); err != nil {
		return nil, fmt.Errorf("ensure storage directories: %w", err)
	}
	return &Manager{cfg: cfg, db: database, ipc: ipc}, nil
}

// Config returns the active configuration.
func (m *Manager) Config() *config.Config {
	return m.cfg
}

// DB returns the underlying database handle.
func (m *Manager) DB() *db.DB {
	return m.db
}

// IPC returns the KiCad IPC client.
func (m *Manager) IPC() *kicad.IPCClient {
	return m.ipc
}

// UpdateConfig swaps configuration (e.g., after settings changes) and ensures directories.
func (m *Manager) UpdateConfig(cfg *config.Config) error {
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	m.cfg = cfg
	if m.ipc != nil {
		m.ipc.SetSocketPath(cfg.KiCadIPCSocket)
	}
	return nil
}

// ImportPart copies raw assets into the immutable vault, links symbol->footprint->3D model,
// saves the part to SQLite, regenerates the target KiCad library, and registers library tables.
func (m *Manager) ImportPart(req ImportPartRequest) (*db.Part, error) {
	if req.LibraryID <= 0 {
		return nil, errors.New("target library_id is required")
	}
	lib, err := m.db.GetLibrary(req.LibraryID)
	if err != nil {
		return nil, fmt.Errorf("lookup target library %d: %w", req.LibraryID, err)
	}

	partName := sanitizeFileComponent(strings.TrimSpace(req.PartName))
	if partName == "" {
		return nil, errors.New("part name cannot be empty")
	}
	if req.Value == "" {
		req.Value = partName
	}

	partUUID := uuid.NewString()
	vaultPartDir := filepath.Join(m.cfg.VaultDir(), "parts", partUUID)
	if err := os.MkdirAll(vaultPartDir, 0o755); err != nil {
		return nil, fmt.Errorf("create vault part dir: %w", err)
	}

	// Copy datasheet if it is a local file
	datasheet := strings.TrimSpace(req.Datasheet)
	if datasheet != "" && !strings.HasPrefix(strings.ToLower(datasheet), "http://") && !strings.HasPrefix(strings.ToLower(datasheet), "https://") {
		if info, err := os.Stat(datasheet); err == nil && !info.IsDir() {
			dst := filepath.Join(vaultPartDir, filepath.Base(datasheet))
			if err := copyFile(datasheet, dst); err == nil {
				datasheet = dst
			}
		}
	}

	meta := copyMap(req.Metadata)
	if req.MPN != "" && meta["MPN"] == "" {
		meta["MPN"] = req.MPN
	}
	if req.Manufacturer != "" && meta["Manufacturer"] == "" {
		meta["Manufacturer"] = req.Manufacturer
	}
	if req.LCSCPart != "" && meta["LCSC"] == "" {
		meta["LCSC"] = req.LCSCPart
	}

	var symModel *db.Symbol
	var fpModel *db.Footprint
	var models3D []db.Model3D

	// Process 3D models into vault first so we know their managed filenames
	for _, m3 := range req.Models3D {
		fname := filepath.Base(m3.FileName)
		if fname == "" || fname == "." {
			fname = filepath.Base(m3.SourcePath)
		}
		if fname == "" || fname == "." {
			continue
		}
		vaultModelPath := filepath.Join(vaultPartDir, fname)
		if m3.SourcePath != "" {
			if err := copyFile(m3.SourcePath, vaultModelPath); err != nil {
				return nil, fmt.Errorf("copy 3D model %q to vault: %w", m3.SourcePath, err)
			}
		}
		format := strings.TrimPrefix(strings.ToLower(filepath.Ext(fname)), ".")
		if format == "stp" {
			format = "step"
		}
		if m3.Format != "" {
			format = strings.ToLower(m3.Format)
		}
		sx, sy, sz := m3.ScaleX, m3.ScaleY, m3.ScaleZ
		if sx == 0 && sy == 0 && sz == 0 {
			sx, sy, sz = 1, 1, 1
		}
		models3D = append(models3D, db.Model3D{
			FileName:  fname,
			Format:    format,
			VaultPath: vaultModelPath,
			OffsetX:   m3.OffsetX, OffsetY: m3.OffsetY, OffsetZ: m3.OffsetZ,
			ScaleX: sx, ScaleY: sy, ScaleZ: sz,
			RotateX: m3.RotateX, RotateY: m3.RotateY, RotateZ: m3.RotateZ,
		})
	}

	// Process footprint
	fpRaw := strings.TrimSpace(req.FootprintSExpr)
	if fpRaw == "" && req.FootprintSourcePath != "" {
		b, err := os.ReadFile(req.FootprintSourcePath)
		if err != nil {
			return nil, fmt.Errorf("read footprint %q: %w", req.FootprintSourcePath, err)
		}
		fpRaw = string(b)
	}
	fpName := sanitizeFileComponent(strings.TrimSpace(req.FootprintName))
	if fpRaw != "" {
		parsedFP, err := kicad.ParseFootprintFile([]byte(fpRaw))
		if err != nil {
			return nil, fmt.Errorf("parse footprint: %w", err)
		}
		if fpName == "" {
			fpName = sanitizeFileComponent(parsedFP.Name)
		}
		if fpName == "" {
			fpName = partName
		}

		vaultFPPath := filepath.Join(vaultPartDir, fpName+".kicad_mod")
		if err := os.WriteFile(vaultFPPath, []byte(fpRaw), 0o644); err != nil {
			return nil, fmt.Errorf("write vault footprint: %w", err)
		}

		fpModel = &db.Footprint{
			Name:      fpName,
			RawSExpr:  fpRaw,
			VaultPath: vaultFPPath,
			PadCount:  parsedFP.PadCount,
		}
	}

	// Process symbol
	symRaw := strings.TrimSpace(req.SymbolSExpr)
	if symRaw == "" && req.SymbolSourcePath != "" {
		b, err := os.ReadFile(req.SymbolSourcePath)
		if err != nil {
			return nil, fmt.Errorf("read symbol %q: %w", req.SymbolSourcePath, err)
		}
		syms, err := kicad.ParseSymbolLibrary(b)
		if err != nil {
			return nil, fmt.Errorf("parse symbol %q: %w", req.SymbolSourcePath, err)
		}
		if len(syms) > 0 {
			selected := syms[0]
			if req.SymbolName != "" {
				for _, s := range syms {
					if s.Name == req.SymbolName {
						selected = s
						break
					}
				}
			}
			symRaw = selected.RawSExpr
			if req.SymbolName == "" {
				req.SymbolName = selected.Name
			}
		}
	}

	if symRaw != "" {
		syms, err := kicad.ParseSymbolLibrary([]byte(symRaw))
		if err != nil {
			return nil, fmt.Errorf("parse symbol sexpr: %w", err)
		}
		if len(syms) == 0 {
			return nil, errors.New("symbol sexpr contained no symbol definition")
		}
		parsedSym := syms[0]

		symName := strings.TrimSpace(req.SymbolName)
		if symName == "" {
			symName = partName
		}

		fpRef := ""
		if fpModel != nil {
			fpRef = lib.Name + ":" + fpModel.Name
		}

		preparedSym, err := kicad.PrepareSymbolNode(
			parsedSym.RawSExpr,
			partName,
			fpRef,
			req.Value,
			datasheet,
			req.Description,
			req.Keywords,
			meta,
		)
		if err != nil {
			return nil, fmt.Errorf("prepare symbol: %w", err)
		}

		vaultSymPath := filepath.Join(vaultPartDir, partName+".kicad_sym")
		if err := os.WriteFile(vaultSymPath, []byte(kicad.BuildSymbolLibraryFile([]string{preparedSym})), 0o644); err != nil {
			return nil, fmt.Errorf("write vault symbol: %w", err)
		}

		symModel = &db.Symbol{
			Name:      partName,
			RawSExpr:  preparedSym,
			VaultPath: vaultSymPath,
			PinCount:  parsedSym.PinCount,
		}
	}

	part := &db.Part{
		UUID:         partUUID,
		LibraryID:    lib.ID,
		LibraryName:  lib.Name,
		Name:         partName,
		Value:        req.Value,
		Description:  req.Description,
		Keywords:     req.Keywords,
		Datasheet:    datasheet,
		MPN:          req.MPN,
		Manufacturer: req.Manufacturer,
		LCSCPart:     req.LCSCPart,
		SourceType:   req.SourceType,
		SourceRef:    req.SourceRef,
		Symbol:       symModel,
		Footprint:    fpModel,
		Models3D:     models3D,
		Metadata:     meta,
	}

	if err := m.db.SavePart(part); err != nil {
		return nil, err
	}

	if err := m.RegenerateLibrary(lib.ID); err != nil {
		return nil, fmt.Errorf("regenerate library %q: %w", lib.Name, err)
	}
	m.notifyIPC()

	return m.db.GetPart(part.ID)
}

// MergeOptions controls how an incoming candidate part resolves against an existing part in the database.
type MergeOptions struct {
	Strategy             string // "merge", "overwrite", "rename", "skip"
	NewName              string // target part name when Strategy == "rename"
	UseIncomingDetails   bool   // prefer incoming Value, Description, Keywords, Datasheet, MPN, Manufacturer, LCSCPart
	UseIncomingSymbol    bool   // replace existing symbol with incoming symbol
	UseIncomingFootprint bool   // replace existing footprint with incoming footprint
	Model3DMode          string // "combine", "keep_existing", "use_incoming"
	MetadataMode         string // "prefer_incoming", "keep_existing", "replace"
}

// MergePart resolves a duplicate import against existingPartID using the specified merge strategy.
func (m *Manager) MergePart(existingPartID int64, req ImportPartRequest, opts MergeOptions) (*db.Part, error) {
	existing, err := m.db.GetPart(existingPartID)
	if err != nil {
		return nil, fmt.Errorf("load existing part %d: %w", existingPartID, err)
	}

	strategy := strings.ToLower(strings.TrimSpace(opts.Strategy))
	if strategy == "" {
		strategy = "merge"
	}

	switch strategy {
	case "skip":
		return existing, nil

	case "rename":
		newName := sanitizeFileComponent(strings.TrimSpace(opts.NewName))
		if newName == "" {
			newName = sanitizeFileComponent(strings.TrimSpace(req.PartName))
		}
		if newName == "" {
			newName = existing.Name
		}
		if req.LibraryID == existing.LibraryID && strings.EqualFold(newName, existing.Name) {
			newName = m.uniquePartNameInLibrary(req.LibraryID, newName)
		} else {
			newName = m.ensureUniquePartNameIfExists(req.LibraryID, newName)
		}
		req.PartName = newName
		return m.ImportPart(req)

	case "overwrite":
		hasIncSym := strings.TrimSpace(req.SymbolSExpr) != "" || strings.TrimSpace(req.SymbolSourcePath) != ""
		hasIncFP := strings.TrimSpace(req.FootprintSExpr) != "" || strings.TrimSpace(req.FootprintSourcePath) != ""
		return m.MergePart(existingPartID, req, MergeOptions{
			Strategy:             "merge",
			UseIncomingDetails:   true,
			UseIncomingSymbol:    hasIncSym,
			UseIncomingFootprint: hasIncFP,
			Model3DMode:          "use_incoming",
			MetadataMode:         "replace",
		})

	case "merge":
		if existing.SourceType == "kicad_sync" {
			if err := m.materializePartToVault(existing); err != nil {
				return nil, fmt.Errorf("materialize synced part to vault: %w", err)
			}
			if req.SourceType != "" && req.SourceType != "kicad_sync" {
				existing.SourceType = req.SourceType
			} else {
				existing.SourceType = "manual"
			}
		}

		lib, err := m.db.GetLibrary(existing.LibraryID)
		if err != nil {
			return nil, fmt.Errorf("lookup target library %d: %w", existing.LibraryID, err)
		}

		vaultPartDir := filepath.Join(m.cfg.VaultDir(), "parts", existing.UUID)
		if err := os.MkdirAll(vaultPartDir, 0o755); err != nil {
			return nil, fmt.Errorf("create vault part dir: %w", err)
		}

		// 1. Scalar details
		existing.Value = pickMergedField(existing.Value, req.Value, opts.UseIncomingDetails)
		existing.Description = pickMergedField(existing.Description, req.Description, opts.UseIncomingDetails)
		existing.Keywords = pickMergedField(existing.Keywords, req.Keywords, opts.UseIncomingDetails)
		existing.MPN = pickMergedField(existing.MPN, req.MPN, opts.UseIncomingDetails)
		existing.Manufacturer = pickMergedField(existing.Manufacturer, req.Manufacturer, opts.UseIncomingDetails)
		existing.LCSCPart = pickMergedField(existing.LCSCPart, req.LCSCPart, opts.UseIncomingDetails)
		chosenDS := pickMergedField(existing.Datasheet, req.Datasheet, opts.UseIncomingDetails)
		if chosenDS != "" && !strings.HasPrefix(strings.ToLower(chosenDS), "http://") && !strings.HasPrefix(strings.ToLower(chosenDS), "https://") {
			if !strings.HasPrefix(filepath.Clean(chosenDS), filepath.Clean(vaultPartDir)+string(os.PathSeparator)) {
				if info, statErr := os.Stat(chosenDS); statErr == nil && !info.IsDir() {
					dst := filepath.Join(vaultPartDir, filepath.Base(chosenDS))
					if copyFile(chosenDS, dst) == nil {
						chosenDS = dst
					}
				}
			}
		}
		existing.Datasheet = chosenDS
		if opts.UseIncomingDetails && strings.TrimSpace(req.SourceRef) != "" {
			existing.SourceRef = req.SourceRef
		}

		// 2. Metadata
		meta := make(map[string]string)
		switch strings.ToLower(strings.TrimSpace(opts.MetadataMode)) {
		case "replace":
			for k, v := range req.Metadata {
				if strings.TrimSpace(k) != "" {
					meta[k] = v
				}
			}
		case "keep_existing":
			for k, v := range req.Metadata {
				if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
					meta[k] = v
				}
			}
			for k, v := range existing.Metadata {
				if strings.TrimSpace(k) != "" {
					meta[k] = v
				}
			}
		default: // "prefer_incoming"
			for k, v := range existing.Metadata {
				if strings.TrimSpace(k) != "" {
					meta[k] = v
				}
			}
			for k, v := range req.Metadata {
				if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
					meta[k] = v
				}
			}
		}
		if existing.MPN != "" && meta["MPN"] == "" {
			meta["MPN"] = existing.MPN
		}
		if existing.Manufacturer != "" && meta["Manufacturer"] == "" {
			meta["Manufacturer"] = existing.Manufacturer
		}
		if existing.LCSCPart != "" && meta["LCSC"] == "" {
			meta["LCSC"] = existing.LCSCPart
		}
		existing.Metadata = meta

		// 3. 3D Models
		var stagedIncomingModels []db.Model3D
		for _, m3 := range req.Models3D {
			fname := filepath.Base(m3.FileName)
			if fname == "" || fname == "." {
				fname = filepath.Base(m3.SourcePath)
			}
			if fname == "" || fname == "." {
				continue
			}
			vaultModelPath := filepath.Join(vaultPartDir, fname)
			if m3.SourcePath != "" {
				if err := copyFile(m3.SourcePath, vaultModelPath); err != nil {
					return nil, fmt.Errorf("copy 3D model %q to vault: %w", m3.SourcePath, err)
				}
			}
			format := strings.TrimPrefix(strings.ToLower(filepath.Ext(fname)), ".")
			if format == "stp" {
				format = "step"
			}
			if m3.Format != "" {
				format = strings.ToLower(m3.Format)
			}
			sx, sy, sz := m3.ScaleX, m3.ScaleY, m3.ScaleZ
			if sx == 0 && sy == 0 && sz == 0 {
				sx, sy, sz = 1, 1, 1
			}
			stagedIncomingModels = append(stagedIncomingModels, db.Model3D{
				FileName:  fname,
				Format:    format,
				VaultPath: vaultModelPath,
				OffsetX:   m3.OffsetX, OffsetY: m3.OffsetY, OffsetZ: m3.OffsetZ,
				ScaleX: sx, ScaleY: sy, ScaleZ: sz,
				RotateX: m3.RotateX, RotateY: m3.RotateY, RotateZ: m3.RotateZ,
			})
		}

		switch strings.ToLower(strings.TrimSpace(opts.Model3DMode)) {
		case "keep_existing":
			if len(existing.Models3D) == 0 && len(stagedIncomingModels) > 0 {
				existing.Models3D = stagedIncomingModels
			}
		case "use_incoming":
			if len(stagedIncomingModels) > 0 {
				existing.Models3D = stagedIncomingModels
			}
		default: // "combine"
			combined := make([]db.Model3D, 0, len(existing.Models3D)+len(stagedIncomingModels))
			idxByName := make(map[string]int, len(existing.Models3D))
			for _, em := range existing.Models3D {
				idxByName[strings.ToLower(em.FileName)] = len(combined)
				combined = append(combined, em)
			}
			for _, im := range stagedIncomingModels {
				k := strings.ToLower(im.FileName)
				if idx, exists := idxByName[k]; exists {
					combined[idx] = im
				} else {
					idxByName[k] = len(combined)
					combined = append(combined, im)
				}
			}
			existing.Models3D = combined
		}

		// 4. Footprint
		hasIncFP := strings.TrimSpace(req.FootprintSExpr) != "" || strings.TrimSpace(req.FootprintSourcePath) != ""
		if (opts.UseIncomingFootprint || existing.Footprint == nil) && hasIncFP {
			fpRaw := strings.TrimSpace(req.FootprintSExpr)
			if fpRaw == "" && req.FootprintSourcePath != "" {
				b, rerr := os.ReadFile(req.FootprintSourcePath)
				if rerr != nil {
					return nil, fmt.Errorf("read footprint %q: %w", req.FootprintSourcePath, rerr)
				}
				fpRaw = string(b)
			}
			parsedFP, perr := kicad.ParseFootprintFile([]byte(fpRaw))
			if perr != nil {
				return nil, fmt.Errorf("parse incoming footprint: %w", perr)
			}
			fpName := sanitizeFileComponent(strings.TrimSpace(req.FootprintName))
			if fpName == "" {
				fpName = sanitizeFileComponent(parsedFP.Name)
			}
			if fpName == "" {
				fpName = existing.Name
			}
			vaultFPPath := filepath.Join(vaultPartDir, fpName+".kicad_mod")
			if err := os.WriteFile(vaultFPPath, []byte(fpRaw), 0o644); err != nil {
				return nil, fmt.Errorf("write vault footprint: %w", err)
			}
			existing.Footprint = &db.Footprint{
				Name:      fpName,
				RawSExpr:  fpRaw,
				VaultPath: vaultFPPath,
				PadCount:  parsedFP.PadCount,
			}
		}

		// 5. Symbol
		hasIncSym := strings.TrimSpace(req.SymbolSExpr) != "" || strings.TrimSpace(req.SymbolSourcePath) != ""
		var symSourceRaw string
		if (opts.UseIncomingSymbol || existing.Symbol == nil) && hasIncSym {
			symSourceRaw = strings.TrimSpace(req.SymbolSExpr)
			if symSourceRaw == "" && req.SymbolSourcePath != "" {
				b, rerr := os.ReadFile(req.SymbolSourcePath)
				if rerr != nil {
					return nil, fmt.Errorf("read symbol %q: %w", req.SymbolSourcePath, rerr)
				}
				syms, perr := kicad.ParseSymbolLibrary(b)
				if perr != nil {
					return nil, fmt.Errorf("parse symbol %q: %w", req.SymbolSourcePath, perr)
				}
				if len(syms) > 0 {
					sel := syms[0]
					for _, s := range syms {
						if s.Name == req.SymbolName {
							sel = s
							break
						}
					}
					symSourceRaw = sel.RawSExpr
				}
			}
		} else if existing.Symbol != nil {
			symSourceRaw = existing.Symbol.RawSExpr
		}

		if symSourceRaw != "" {
			syms, perr := kicad.ParseSymbolLibrary([]byte(symSourceRaw))
			if perr != nil {
				return nil, fmt.Errorf("parse symbol sexpr: %w", perr)
			}
			if len(syms) == 0 {
				return nil, errors.New("symbol sexpr contained no symbol definition")
			}
			parsedSym := syms[0]
			fpRef := ""
			if existing.Footprint != nil {
				fpRef = lib.Name + ":" + existing.Footprint.Name
			}
			preparedSym, perr := kicad.PrepareSymbolNode(
				parsedSym.RawSExpr,
				existing.Name,
				fpRef,
				existing.Value,
				existing.Datasheet,
				existing.Description,
				existing.Keywords,
				meta,
			)
			if perr != nil {
				return nil, fmt.Errorf("prepare merged symbol: %w", perr)
			}
			vaultSymPath := filepath.Join(vaultPartDir, sanitizeFileComponent(existing.Name)+".kicad_sym")
			if err := os.WriteFile(vaultSymPath, []byte(kicad.BuildSymbolLibraryFile([]string{preparedSym})), 0o644); err != nil {
				return nil, fmt.Errorf("write vault symbol: %w", err)
			}
			existing.Symbol = &db.Symbol{
				Name:      existing.Name,
				RawSExpr:  preparedSym,
				VaultPath: vaultSymPath,
				PinCount:  parsedSym.PinCount,
			}
		}

		if err := m.db.SavePart(existing); err != nil {
			return nil, err
		}
		if err := m.RegenerateLibrary(existing.LibraryID); err != nil {
			return nil, fmt.Errorf("regenerate library %q: %w", lib.Name, err)
		}
		m.invalidatePreviewCache(existing.UUID)
		m.notifyIPC()
		return m.db.GetPart(existing.ID)

	default:
		return nil, fmt.Errorf("unknown merge strategy %q", opts.Strategy)
	}
}

func pickMergedField(existingVal, incomingVal string, preferIncoming bool) string {
	cur := strings.TrimSpace(existingVal)
	inc := strings.TrimSpace(incomingVal)
	if preferIncoming {
		if inc != "" {
			return incomingVal
		}
		return existingVal
	}
	if cur != "" {
		return existingVal
	}
	return incomingVal
}

func (m *Manager) uniquePartNameInLibrary(libraryID int64, base string) string {
	existing, err := m.db.ListPartsByLibrary(libraryID)
	if err != nil {
		return base + "_2"
	}
	used := make(map[string]bool, len(existing))
	for _, p := range existing {
		used[strings.ToLower(p.Name)] = true
	}
	for n := 2; n < 10000; n++ {
		cand := fmt.Sprintf("%s_%d", base, n)
		if !used[strings.ToLower(cand)] {
			return cand
		}
	}
	return base + "_" + uuid.NewString()[:8]
}

func (m *Manager) ensureUniquePartNameIfExists(libraryID int64, candidate string) string {
	existing, err := m.db.ListPartsByLibrary(libraryID)
	if err != nil {
		return candidate
	}
	used := make(map[string]bool, len(existing))
	for _, p := range existing {
		used[strings.ToLower(p.Name)] = true
	}
	if !used[strings.ToLower(candidate)] {
		return candidate
	}
	return m.uniquePartNameInLibrary(libraryID, candidate)
}

// RegenerateLibrary rebuilds the .kicad_sym, .pretty, and .3dshapes files on disk for libraryID
// from the SQLite database + immutable vault, and registers the library in KiCad's tables.
func (m *Manager) RegenerateLibrary(libraryID int64) error {
	lib, err := m.db.GetLibrary(libraryID)
	if err != nil {
		return err
	}
	parts, err := m.db.ListPartsByLibrary(libraryID)
	if err != nil {
		return err
	}

	symFilePath := filepath.Join(m.cfg.SymbolsDir(), lib.Name+".kicad_sym")
	prettyDir := filepath.Join(m.cfg.FootprintsDir(), lib.Name+".pretty")
	shapesDir := filepath.Join(m.cfg.Models3DDir(), lib.Name+".3dshapes")

	// Rebuild .pretty and .3dshapes cleanly so moved/deleted parts leave no stale files
	_ = os.RemoveAll(prettyDir)
	_ = os.RemoveAll(shapesDir)
	if err := os.MkdirAll(m.cfg.SymbolsDir(), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(prettyDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(shapesDir, 0o755); err != nil {
		return err
	}

	var symBlocks []string
	for _, p := range parts {
		var modelPaths []kicad.ModelTransform
		for _, mod := range p.Models3D {
			if mod.VaultPath == "" || mod.FileName == "" {
				continue
			}
			if _, statErr := os.Stat(mod.VaultPath); statErr != nil {
				continue
			}
			targetShapePath := filepath.Join(shapesDir, mod.FileName)
			if err := copyFile(mod.VaultPath, targetShapePath); err != nil {
				return fmt.Errorf("copy vault 3D model %q: %w", mod.VaultPath, err)
			}
			modelPaths = append(modelPaths, kicad.ModelTransform{
				Path:    targetShapePath,
				OffsetX: mod.OffsetX, OffsetY: mod.OffsetY, OffsetZ: mod.OffsetZ,
				ScaleX: mod.ScaleX, ScaleY: mod.ScaleY, ScaleZ: mod.ScaleZ,
				RotateX: mod.RotateX, RotateY: mod.RotateY, RotateZ: mod.RotateZ,
			})
		}

		fpRef := ""
		if p.Footprint != nil && p.Footprint.RawSExpr != "" {
			fpName := sanitizeFileComponent(p.Footprint.Name)
			if fpName == "" {
				fpName = p.Name
			}
			fpRef = lib.Name + ":" + fpName

			rewrittenFP, err := kicad.RewriteFootprint(p.Footprint.RawSExpr, fpName, modelPaths)
			if err != nil {
				return fmt.Errorf("rewrite footprint for %q: %w", p.Name, err)
			}
			fpFile := filepath.Join(prettyDir, fpName+".kicad_mod")
			if err := os.WriteFile(fpFile, []byte(rewrittenFP), 0o644); err != nil {
				return fmt.Errorf("write footprint %q: %w", fpFile, err)
			}
		}

		if p.Symbol != nil && p.Symbol.RawSExpr != "" {
			meta := copyMap(p.Metadata)
			if p.MPN != "" && meta["MPN"] == "" {
				meta["MPN"] = p.MPN
			}
			if p.Manufacturer != "" && meta["Manufacturer"] == "" {
				meta["Manufacturer"] = p.Manufacturer
			}
			if p.LCSCPart != "" && meta["LCSC"] == "" {
				meta["LCSC"] = p.LCSCPart
			}
			preparedSym, err := kicad.PrepareSymbolNode(
				p.Symbol.RawSExpr,
				p.Name,
				fpRef,
				p.Value,
				p.Datasheet,
				p.Description,
				p.Keywords,
				meta,
			)
			if err != nil {
				return fmt.Errorf("prepare symbol for %q: %w", p.Name, err)
			}
			symBlocks = append(symBlocks, preparedSym)
		}
	}

	symLibContent := kicad.BuildSymbolLibraryFile(symBlocks)
	if err := os.WriteFile(symFilePath, []byte(symLibContent), 0o644); err != nil {
		return fmt.Errorf("write symbol library %q: %w", symFilePath, err)
	}

	hasManagedParts := lib.SourceURI == ""
	for _, p := range parts {
		if p.SourceType != "kicad_sync" {
			hasManagedParts = true
			break
		}
	}

	if m.cfg.AutoRegisterLibTables && m.cfg.KiCadConfigDir != "" && hasManagedParts {
		if err := kicad.RegisterSymbolLibrary(m.cfg.KiCadConfigDir, lib.Name, symFilePath, lib.Description); err != nil {
			return fmt.Errorf("register sym-lib-table: %w", err)
		}
		if err := kicad.RegisterFootprintLibrary(m.cfg.KiCadConfigDir, lib.Name, prettyDir, lib.Description); err != nil {
			return fmt.Errorf("register fp-lib-table: %w", err)
		}
	}

	return nil
}

// RegenerateAllLibraries rebuilds every managed library on disk from SQLite + vault and notifies KiCad IPC.
func (m *Manager) RegenerateAllLibraries() error {
	libs, err := m.db.ListLibraries()
	if err != nil {
		return err
	}
	for _, lib := range libs {
		if lib.SourceURI != "" {
			// Skip untouched external synced KiCad libraries unless they contain user-managed parts
			parts, perr := m.db.ListPartsByLibrary(lib.ID)
			if perr != nil {
				return perr
			}
			hasManaged := false
			for _, p := range parts {
				if p.SourceType != "kicad_sync" {
					hasManaged = true
					break
				}
			}
			if !hasManaged {
				continue
			}
		}
		if err := m.RegenerateLibrary(lib.ID); err != nil {
			return err
		}
	}
	m.notifyIPC()
	return nil
}

// MovePart moves a part to targetLibraryID, updates DB, and regenerates affected libraries.
func (m *Manager) MovePart(partID, targetLibraryID int64) error {
	part, err := m.db.GetPart(partID)
	if err != nil {
		return err
	}
	oldLibID := part.LibraryID
	if oldLibID == targetLibraryID {
		return nil
	}

	if part.SourceType == "kicad_sync" {
		if err := m.materializePartToVault(part); err != nil {
			return err
		}
		part.SourceType = "manual"
		part.LibraryID = targetLibraryID
		part.LibraryName = ""
		if err := m.db.SavePart(part); err != nil {
			return err
		}
	} else {
		if err := m.db.MovePart(partID, targetLibraryID); err != nil {
			return err
		}
	}

	if oldLib, err := m.db.GetLibrary(oldLibID); err == nil && oldLib.SourceURI == "" {
		if err := m.RegenerateLibrary(oldLibID); err != nil {
			return fmt.Errorf("regenerate source library: %w", err)
		}
	}
	if err := m.RegenerateLibrary(targetLibraryID); err != nil {
		return fmt.Errorf("regenerate target library: %w", err)
	}
	m.invalidatePreviewCache(part.UUID)
	m.notifyIPC()
	return nil
}

// UpdatePart saves edits to a part (including metadata) and regenerates its KiCad library files.
func (m *Manager) UpdatePart(part *db.Part) error {
	if part.SourceType == "kicad_sync" {
		if err := m.materializePartToVault(part); err != nil {
			return err
		}
		part.SourceType = "manual"
	}
	if err := m.db.SavePart(part); err != nil {
		return err
	}
	if err := m.RegenerateLibrary(part.LibraryID); err != nil {
		return err
	}
	m.invalidatePreviewCache(part.UUID)
	m.notifyIPC()
	return nil
}

func (m *Manager) materializePartToVault(part *db.Part) error {
	vaultDir := filepath.Join(m.cfg.VaultDir(), "parts", part.UUID)
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		return err
	}
	if part.Symbol != nil && part.Symbol.RawSExpr != "" {
		symVault := filepath.Join(vaultDir, sanitizeFileComponent(part.Name)+".kicad_sym")
		_ = os.WriteFile(symVault, []byte(kicad.BuildSymbolLibraryFile([]string{part.Symbol.RawSExpr})), 0o644)
		part.Symbol.VaultPath = symVault
	}
	if part.Footprint != nil && part.Footprint.RawSExpr != "" {
		fpVault := filepath.Join(vaultDir, sanitizeFileComponent(part.Footprint.Name)+".kicad_mod")
		_ = os.WriteFile(fpVault, []byte(part.Footprint.RawSExpr), 0o644)
		part.Footprint.VaultPath = fpVault
	}
	for i := range part.Models3D {
		mod := &part.Models3D[i]
		if mod.VaultPath == "" || mod.FileName == "" {
			continue
		}
		dst := filepath.Join(vaultDir, mod.FileName)
		if dst == mod.VaultPath {
			continue
		}
		if _, err := os.Stat(mod.VaultPath); err == nil {
			if err := copyFile(mod.VaultPath, dst); err == nil {
				mod.VaultPath = dst
			}
		}
	}
	return nil
}

// Attach3DModel copies a 3D model file into the part's vault, associates it with the part, and regenerates its library.
func (m *Manager) Attach3DModel(partID int64, sourcePath string) error {
	part, err := m.db.GetPart(partID)
	if err != nil {
		return err
	}
	fname := filepath.Base(sourcePath)
	vaultDir := filepath.Join(m.cfg.VaultDir(), "parts", part.UUID)
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(vaultDir, fname)
	if err := copyFile(sourcePath, dst); err != nil {
		return err
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(fname)), ".")
	if format == "stp" {
		format = "step"
	}
	var fpID int64
	if part.Footprint != nil {
		fpID = part.Footprint.ID
	}
	part.Models3D = append(part.Models3D, db.Model3D{
		PartID:      part.ID,
		FootprintID: fpID,
		FileName:    fname,
		Format:      format,
		VaultPath:   dst,
		ScaleX:      1, ScaleY: 1, ScaleZ: 1,
	})
	return m.UpdatePart(part)
}

// DeletePart deletes a part from SQLite, cleans up its vault directory, and regenerates its library.
func (m *Manager) DeletePart(partID int64) error {
	part, err := m.db.GetPart(partID)
	if err != nil {
		return err
	}
	if err := m.db.DeletePart(partID); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(m.cfg.VaultDir(), "parts", part.UUID))
	m.invalidatePreviewCache(part.UUID)
	if err := m.RegenerateLibrary(part.LibraryID); err != nil {
		return err
	}
	m.notifyIPC()
	return nil
}

// DeleteLibrary removes a library from SQLite, deletes its synced files, and unregisters KiCad library tables.
func (m *Manager) DeleteLibrary(libraryID int64) error {
	lib, err := m.db.GetLibrary(libraryID)
	if err != nil {
		return err
	}
	parts, _ := m.db.ListPartsByLibrary(libraryID)
	if err := m.db.DeleteLibrary(libraryID); err != nil {
		return err
	}
	for _, p := range parts {
		_ = os.RemoveAll(filepath.Join(m.cfg.VaultDir(), "parts", p.UUID))
		m.invalidatePreviewCache(p.UUID)
	}
	_ = os.Remove(filepath.Join(m.cfg.SymbolsDir(), lib.Name+".kicad_sym"))
	_ = os.RemoveAll(filepath.Join(m.cfg.FootprintsDir(), lib.Name+".pretty"))
	_ = os.RemoveAll(filepath.Join(m.cfg.Models3DDir(), lib.Name+".3dshapes"))
	if m.cfg.AutoRegisterLibTables && m.cfg.KiCadConfigDir != "" {
		_ = kicad.UnregisterLibrary(m.cfg.KiCadConfigDir, lib.Name)
	}
	m.notifyIPC()
	return nil
}

// ExportLibraryArchive packages a standalone portable KiCad library .zip using `${KIPRJMOD}` relative 3D model paths.
func (m *Manager) ExportLibraryArchive(libraryID int64, destZipPath string) error {
	lib, err := m.db.GetLibrary(libraryID)
	if err != nil {
		return err
	}
	parts, err := m.db.ListPartsByLibrary(libraryID)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(destZipPath), 0o755); err != nil {
		return err
	}
	outFile, err := os.Create(destZipPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	zw := zip.NewWriter(outFile)
	defer zw.Close()

	var symBlocks []string
	addedModels := make(map[string]bool)

	for _, p := range parts {
		var relModels []kicad.ModelTransform
		for _, mod := range p.Models3D {
			if mod.VaultPath == "" || mod.FileName == "" {
				continue
			}
			relPathInZip := filepath.ToSlash(filepath.Join(lib.Name+".3dshapes", mod.FileName))
			if !addedModels[relPathInZip] {
				if err := addFileToZip(zw, mod.VaultPath, relPathInZip); err != nil {
					return err
				}
				addedModels[relPathInZip] = true
			}
			relModels = append(relModels, kicad.ModelTransform{
				Path:    "${KIPRJMOD}/" + relPathInZip,
				OffsetX: mod.OffsetX, OffsetY: mod.OffsetY, OffsetZ: mod.OffsetZ,
				ScaleX: mod.ScaleX, ScaleY: mod.ScaleY, ScaleZ: mod.ScaleZ,
				RotateX: mod.RotateX, RotateY: mod.RotateY, RotateZ: mod.RotateZ,
			})
		}

		fpRef := ""
		if p.Footprint != nil && p.Footprint.RawSExpr != "" {
			fpName := sanitizeFileComponent(p.Footprint.Name)
			if fpName == "" {
				fpName = p.Name
			}
			fpRef = lib.Name + ":" + fpName
			rewrittenFP, err := kicad.RewriteFootprint(p.Footprint.RawSExpr, fpName, relModels)
			if err != nil {
				return err
			}
			fpEntry := filepath.ToSlash(filepath.Join(lib.Name+".pretty", fpName+".kicad_mod"))
			if err := addBytesToZip(zw, []byte(rewrittenFP), fpEntry); err != nil {
				return err
			}
		}

		if p.Symbol != nil && p.Symbol.RawSExpr != "" {
			preparedSym, err := kicad.PrepareSymbolNode(
				p.Symbol.RawSExpr, p.Name, fpRef, p.Value, p.Datasheet, p.Description, p.Keywords, p.Metadata,
			)
			if err != nil {
				return err
			}
			symBlocks = append(symBlocks, preparedSym)
		}
	}

	symContent := kicad.BuildSymbolLibraryFile(symBlocks)
	if err := addBytesToZip(zw, []byte(symContent), lib.Name+".kicad_sym"); err != nil {
		return err
	}

	// Include portable project sym-lib-table / fp-lib-table snippets
	symTable := fmt.Sprintf("(sym_lib_table\n\t(version 7)\n\t(lib (name %q) (type \"KiCad\") (uri \"${KIPRJMOD}/%s.kicad_sym\") (options \"\") (descr %q))\n)\n",
		lib.Name, lib.Name, lib.Description)
	fpTable := fmt.Sprintf("(fp_lib_table\n\t(version 7)\n\t(lib (name %q) (type \"KiCad\") (uri \"${KIPRJMOD}/%s.pretty\") (options \"\") (descr %q))\n)\n",
		lib.Name, lib.Name, lib.Description)
	if err := addBytesToZip(zw, []byte(symTable), "sym-lib-table"); err != nil {
		return err
	}
	if err := addBytesToZip(zw, []byte(fpTable), "fp-lib-table"); err != nil {
		return err
	}

	return zw.Close()
}

// RenderPartPreviews renders symbol and footprint SVG files for a part using kicad-cli (with caching).
func (m *Manager) RenderPartPreviews(part *db.Part) (symSVGPath, fpSVGPath string, err error) {
	if part == nil || m.cfg.KiCadCLIPath == "" {
		return "", "", nil
	}
	cacheDir := filepath.Join(m.cfg.PreviewCacheDir(), part.UUID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", "", err
	}

	if part.Symbol != nil {
		symDir := filepath.Join(cacheDir, "sym")
		symLibFile := filepath.Join(m.cfg.SymbolsDir(), part.LibraryName+".kicad_sym")
		targetSymFile := ""
		if _, statErr := os.Stat(symLibFile); statErr == nil && part.SourceType != "kicad_sync" {
			targetSymFile = symLibFile
		} else if part.Symbol.VaultPath != "" {
			if _, statErr := os.Stat(part.Symbol.VaultPath); statErr == nil {
				targetSymFile = part.Symbol.VaultPath
			}
		}
		if targetSymFile == "" && part.Symbol.RawSExpr != "" {
			tmpSym := filepath.Join(cacheDir, "preview.kicad_sym")
			if os.WriteFile(tmpSym, []byte(kicad.BuildSymbolLibraryFile([]string{part.Symbol.RawSExpr})), 0o644) == nil {
				targetSymFile = tmpSym
			}
		}
		if targetSymFile != "" {
			if p, e := kicad.ExportSymbolSVG(m.cfg.KiCadCLIPath, targetSymFile, part.Name, symDir); e == nil {
				symSVGPath = p
			}
		}
	}

	if part.Footprint != nil {
		fpDir := filepath.Join(cacheDir, "fp")
		prettyDir := filepath.Join(m.cfg.FootprintsDir(), part.LibraryName+".pretty")
		managedFPFile := filepath.Join(prettyDir, sanitizeFileComponent(part.Footprint.Name)+".kicad_mod")
		targetPrettyDir := ""
		if _, statErr := os.Stat(managedFPFile); statErr == nil && part.SourceType != "kicad_sync" {
			targetPrettyDir = prettyDir
		} else if part.Footprint.VaultPath != "" {
			if _, statErr := os.Stat(part.Footprint.VaultPath); statErr == nil {
				parentDir := filepath.Dir(part.Footprint.VaultPath)
				if strings.HasSuffix(strings.ToLower(parentDir), ".pretty") {
					targetPrettyDir = parentDir
				} else {
					tmpPretty := filepath.Join(cacheDir, "tmp.pretty")
					_ = os.MkdirAll(tmpPretty, 0o755)
					dstFP := filepath.Join(tmpPretty, sanitizeFileComponent(part.Footprint.Name)+".kicad_mod")
					if copyFile(part.Footprint.VaultPath, dstFP) == nil {
						targetPrettyDir = tmpPretty
					}
				}
			}
		}
		if targetPrettyDir == "" && part.Footprint.RawSExpr != "" {
			tmpPretty := filepath.Join(cacheDir, "tmp.pretty")
			_ = os.MkdirAll(tmpPretty, 0o755)
			dstFP := filepath.Join(tmpPretty, sanitizeFileComponent(part.Footprint.Name)+".kicad_mod")
			if os.WriteFile(dstFP, []byte(part.Footprint.RawSExpr), 0o644) == nil {
				targetPrettyDir = tmpPretty
			}
		}
		if targetPrettyDir != "" {
			if p, e := kicad.ExportFootprintSVG(m.cfg.KiCadCLIPath, targetPrettyDir, part.Footprint.Name, fpDir); e == nil {
				fpSVGPath = p
			}
		}
	}
	return symSVGPath, fpSVGPath, nil
}

// SyncFromKiCad discovers all existing KiCad libraries from KiCad IPC (GetPaths, GetOpenDocuments)
// and KiCad's global/project sym-lib-table and fp-lib-table, syncing them into SQLite.
func (m *Manager) SyncFromKiCad(onProgress func(SyncProgress)) (libsSynced int, partsSynced int, err error) {
	if !m.syncing.CompareAndSwap(false, true) {
		return 0, 0, nil
	}
	defer m.syncing.Store(false)

	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	var ipcStatus kicad.IPCStatus
	if m.ipc != nil {
		ipcStatus = m.ipc.CheckStatus()
	}

	symEntries, fpEntries, vars, err := kicad.LoadEffectiveLibTables(m.cfg.KiCadConfigDir, m.cfg.KiCadCLIPath, &ipcStatus, true)
	if err != nil {
		return 0, 0, fmt.Errorf("load KiCad library tables: %w", err)
	}

	managedRoot := filepath.Clean(m.cfg.LibrariesDir())
	isManagedPath := func(p string) bool {
		if p == "" {
			return false
		}
		clean := filepath.Clean(p)
		return clean == managedRoot || strings.HasPrefix(clean, managedRoot+string(os.PathSeparator))
	}

	// Filter out libraries generated by kicad-lib itself and libraries missing on disk
	var activeSymEntries []kicad.LibTableEntry
	for _, se := range symEntries {
		if isManagedPath(se.ResolvedPath) {
			continue
		}
		if info, statErr := os.Stat(se.ResolvedPath); statErr == nil && !info.IsDir() {
			activeSymEntries = append(activeSymEntries, se)
		}
	}

	fpDirByNick := make(map[string]string, len(fpEntries))
	for _, fe := range fpEntries {
		if isManagedPath(fe.ResolvedPath) {
			continue
		}
		if info, statErr := os.Stat(fe.ResolvedPath); statErr == nil && info.IsDir() {
			fpDirByNick[fe.Name] = fe.ResolvedPath
		}
	}

	fpCache := make(map[string]*kicad.ParsedFootprint)
	total := len(activeSymEntries)

	for idx, se := range activeSymEntries {
		info, statErr := os.Stat(se.ResolvedPath)
		if statErr != nil {
			continue
		}
		mtime := info.ModTime().UnixNano()

		upToDate, _ := m.db.IsLibrarySyncUpToDate(se.Name, se.ResolvedPath, mtime)
		if upToDate {
			if onProgress != nil {
				onProgress(SyncProgress{Current: idx + 1, Total: total, LibraryName: se.Name, PartsSynced: partsSynced})
			}
			continue
		}

		data, rerr := os.ReadFile(se.ResolvedPath)
		if rerr != nil {
			continue
		}
		parsedSyms, perr := kicad.ParseSymbolLibraryFast(data)
		if perr != nil || len(parsedSyms) == 0 {
			continue
		}

		dbParts := make([]db.Part, 0, len(parsedSyms))
		matchedFPDir := fpDirByNick[se.Name]

		for _, ps := range parsedSyms {
			var fpModel *db.Footprint
			var models3D []db.Model3D

			if ps.Footprint != "" {
				fpLibNick, fpName := splitLibID(ps.Footprint)
				fpDir := fpDirByNick[fpLibNick]
				if fpDir == "" && fpLibNick == "" {
					fpDir = fpDirByNick[se.Name]
				}
				if fpDir != "" && fpName != "" {
					fpFile := filepath.Join(fpDir, fpName+".kicad_mod")
					if matchedFPDir == "" {
						matchedFPDir = fpDir
					}
					pf := loadParsedFootprintCached(fpFile, fpCache)
					if pf != nil {
						fpModel = &db.Footprint{
							Name:      fpName,
							VaultPath: fpFile,
							PadCount:  pf.PadCount,
						}
						for _, mod := range pf.Models {
							resolvedModPath := kicad.Resolve3DModelPath(kicad.ExpandKiCadURI(mod.Path, vars))
							ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(resolvedModPath)), ".")
							if ext == "stp" {
								ext = "step"
							}
							models3D = append(models3D, db.Model3D{
								FileName:  filepath.Base(resolvedModPath),
								Format:    ext,
								VaultPath: resolvedModPath,
								OffsetX:   mod.OffsetX, OffsetY: mod.OffsetY, OffsetZ: mod.OffsetZ,
								ScaleX: mod.ScaleX, ScaleY: mod.ScaleY, ScaleZ: mod.ScaleZ,
								RotateX: mod.RotateX, RotateY: mod.RotateY, RotateZ: mod.RotateZ,
							})
						}
					} else {
						fpModel = &db.Footprint{
							Name:      fpName,
							VaultPath: fpFile,
						}
					}
				} else if fpName != "" {
					fpModel = &db.Footprint{
						Name: fpName,
					}
				}
			}

			meta := make(map[string]string, len(ps.Properties))
			var mpn, mfg, lcsc string
			for k, v := range ps.Properties {
				meta[k] = v
				switch strings.ToLower(k) {
				case "mpn", "manufacturer_part_number", "part number", "part_number", "mfr. no", "mfg part":
					if mpn == "" {
						mpn = v
					}
				case "manufacturer", "mfr", "mf", "mfg":
					if mfg == "" {
						mfg = v
					}
				case "lcsc", "lcsc part", "lcsc_part":
					if lcsc == "" {
						lcsc = v
					}
				}
			}

			dbParts = append(dbParts, db.Part{
				Name:         ps.Name,
				Value:        ps.Value,
				Description:  ps.Description,
				Keywords:     ps.Keywords,
				Datasheet:    ps.Datasheet,
				MPN:          mpn,
				Manufacturer: mfg,
				LCSCPart:     lcsc,
				SourceType:   "kicad_sync",
				SourceRef:    se.ResolvedPath,
				Symbol: &db.Symbol{
					Name:      ps.Name,
					VaultPath: se.ResolvedPath,
					PinCount:  ps.PinCount,
				},
				Footprint: fpModel,
				Models3D:  models3D,
				Metadata:  meta,
			})
		}

		_, changed, berr := m.db.BulkSyncLibrary(se.Name, se.Description, se.ResolvedPath, matchedFPDir, mtime, dbParts)
		if berr != nil {
			continue
		}
		if changed {
			libsSynced++
			partsSynced += len(dbParts)
		}
		if onProgress != nil {
			onProgress(SyncProgress{
				Current:     idx + 1,
				Total:       total,
				LibraryName: se.Name,
				PartsSynced: partsSynced,
			})
		}
	}

	return libsSynced, partsSynced, nil
}

func splitLibID(ref string) (string, string) {
	ref = strings.TrimSpace(ref)
	if idx := strings.IndexByte(ref, ':'); idx >= 0 {
		return ref[:idx], ref[idx+1:]
	}
	return "", ref
}

func loadParsedFootprintCached(fpPath string, cache map[string]*kicad.ParsedFootprint) *kicad.ParsedFootprint {
	if pf, ok := cache[fpPath]; ok {
		return pf
	}
	data, err := os.ReadFile(fpPath)
	if err != nil {
		cache[fpPath] = nil
		return nil
	}
	pf, err := kicad.ParseFootprintFile(data)
	if err != nil {
		cache[fpPath] = nil
		return nil
	}
	cache[fpPath] = pf
	return pf
}

func (m *Manager) invalidatePreviewCache(partUUID string) {
	if partUUID == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(m.cfg.PreviewCacheDir(), partUUID))
}

func (m *Manager) notifyIPC() {
	if m.ipc != nil {
		go m.ipc.RefreshLibrariesAndNotify()
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func addFileToZip(zw *zip.Writer, srcPath, zipEntry string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	return addBytesToZip(zw, data, zipEntry)
}

func addBytesToZip(zw *zip.Writer, data []byte, zipEntry string) error {
	w, err := zw.Create(zipEntry)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func copyMap(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func sanitizeFileComponent(s string) string {
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "\x00", "")
	return strings.TrimSpace(repl.Replace(s))
}
