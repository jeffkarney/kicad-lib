package storage

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeffk/kicad-lib/internal/config"
	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/kicad"
)

func TestStorageLifecycleMoveSearchRegenerateAndExport(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		StorageRoot:           filepath.Join(tmp, "storage"),
		KiCadConfigDir:        filepath.Join(tmp, "kicad-cfg"),
		AutoRegisterLibTables: true,
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mgr, err := NewManager(cfg, database, nil)
	if err != nil {
		t.Fatal(err)
	}

	libA, err := database.CreateLibrary("Analog_ICs", "OpAmps and Regulators")
	if err != nil {
		t.Fatal(err)
	}
	libB, err := database.CreateLibrary("Power_ICs", "Power Management")
	if err != nil {
		t.Fatal(err)
	}

	// Stage source files
	srcDir := filepath.Join(tmp, "dropped")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stepSrc := filepath.Join(srcDir, "SOT23-5.step")
	if err := os.WriteFile(stepSrc, []byte("ISO-10303-21;\nEND-ISO-10303-21;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	symSExpr := `(symbol "TLV75533"
		(in_bom yes)
		(on_board yes)
		(property "Reference" "U" (at 0 2.54 0) (effects (font (size 1.27 1.27))))
		(property "Value" "TLV75533" (at 0 -2.54 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(symbol "TLV75533_1_1"
			(pin input line (at -5.08 0 0) (length 2.54) (name "IN" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		)
	)`
	fpSExpr := `(footprint "SOT-23-5"
		(layer "F.Cu")
		(attr smd)
		(pad "1" smd rect (at -1 0) (size 0.6 0.9) (layers "F.Cu" "F.Mask" "F.Paste"))
	)`

	imported, err := mgr.ImportPart(ImportPartRequest{
		LibraryID:      libA.ID,
		PartName:       "TLV75533",
		Value:          "3.3V LDO",
		Description:    "500mA Low-Dropout Regulator",
		MPN:            "TLV75533PDBVR",
		Manufacturer:   "Texas Instruments",
		SourceType:     "drop",
		SymbolName:     "TLV75533",
		SymbolSExpr:    symSExpr,
		FootprintName:  "SOT-23-5",
		FootprintSExpr: fpSExpr,
		Models3D: []ImportModel3D{
			{SourcePath: stepSrc, FileName: "SOT23-5.step", Format: "step"},
		},
		Metadata: map[string]string{
			"Package":    "SOT-23-5",
			"OutputV":    "3.3V",
			"MaxCurrent": "500mA",
		},
	})
	if err != nil {
		t.Fatalf("ImportPart failed: %v", err)
	}

	// Verify vault copy
	if _, err := os.Stat(imported.Models3D[0].VaultPath); err != nil {
		t.Fatalf("expected vault 3D model at %s: %v", imported.Models3D[0].VaultPath, err)
	}

	// Verify generated library files in libA
	symLibA := filepath.Join(cfg.SymbolsDir(), "Analog_ICs.kicad_sym")
	symBytesA, err := os.ReadFile(symLibA)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(symBytesA), `(property "Footprint" "Analog_ICs:SOT-23-5"`) {
		t.Fatalf("expected linked footprint in Analog_ICs.kicad_sym, got:\n%s", string(symBytesA))
	}
	if !strings.Contains(string(symBytesA), `(property "MaxCurrent" "500mA"`) {
		t.Fatalf("expected custom metadata in Analog_ICs.kicad_sym")
	}

	fpFileA := filepath.Join(cfg.FootprintsDir(), "Analog_ICs.pretty", "SOT-23-5.kicad_mod")
	fpBytesA, err := os.ReadFile(fpFileA)
	if err != nil {
		t.Fatal(err)
	}
	expectedShapeA := filepath.Join(cfg.Models3DDir(), "Analog_ICs.3dshapes", "SOT23-5.step")
	if !strings.Contains(string(fpBytesA), expectedShapeA) {
		t.Fatalf("expected 3D model path %q in footprint, got:\n%s", expectedShapeA, string(fpBytesA))
	}
	if _, err := os.Stat(expectedShapeA); err != nil {
		t.Fatalf("expected copied 3D model at %s: %v", expectedShapeA, err)
	}

	// Verify sym-lib-table & fp-lib-table registered
	symTbl, err := os.ReadFile(filepath.Join(cfg.KiCadConfigDir, "sym-lib-table"))
	if err != nil || !strings.Contains(string(symTbl), `"Analog_ICs"`) {
		t.Fatalf("expected Analog_ICs in sym-lib-table, err=%v", err)
	}

	// Search by metadata
	found, err := database.SearchParts(db.SearchFilter{
		Query:           "500mA",
		MetadataFilters: map[string]string{"OutputV": "3.3V"},
	})
	if err != nil || len(found) != 1 {
		t.Fatalf("expected 1 search result, got %d (err=%v)", len(found), err)
	}

	// Move part from libA to libB
	if err := mgr.MovePart(imported.ID, libB.ID); err != nil {
		t.Fatalf("MovePart failed: %v", err)
	}

	// Verify old library footprint/3D cleaned up and new library populated with updated link
	if _, err := os.Stat(fpFileA); !os.IsNotExist(err) {
		t.Fatalf("expected old footprint %s removed after move", fpFileA)
	}
	symBytesB, err := os.ReadFile(filepath.Join(cfg.SymbolsDir(), "Power_ICs.kicad_sym"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(symBytesB), `(property "Footprint" "Power_ICs:SOT-23-5"`) {
		t.Fatalf("expected updated Footprint link Power_ICs:SOT-23-5 after move, got:\n%s", string(symBytesB))
	}
	fpBytesB, err := os.ReadFile(filepath.Join(cfg.FootprintsDir(), "Power_ICs.pretty", "SOT-23-5.kicad_mod"))
	if err != nil {
		t.Fatal(err)
	}
	expectedShapeB := filepath.Join(cfg.Models3DDir(), "Power_ICs.3dshapes", "SOT23-5.step")
	if !strings.Contains(string(fpBytesB), expectedShapeB) {
		t.Fatalf("expected updated 3D model path %q after move", expectedShapeB)
	}

	// Delete generated libraries from disk, then regenerate all from DB + vault
	_ = os.RemoveAll(cfg.LibrariesDir())
	if err := mgr.RegenerateAllLibraries(); err != nil {
		t.Fatalf("RegenerateAllLibraries failed: %v", err)
	}
	if _, err := os.Stat(expectedShapeB); err != nil {
		t.Fatalf("expected regenerated 3D model at %s: %v", expectedShapeB, err)
	}
	syms, err := kicad.ParseSymbolLibrary(symBytesB)
	if err != nil || len(syms) != 1 {
		t.Fatalf("ParseSymbolLibrary failed: %v", err)
	}

	// Export portable library archive
	zipOut := filepath.Join(tmp, "Power_ICs_export.zip")
	if err := mgr.ExportLibraryArchive(libB.ID, zipOut); err != nil {
		t.Fatalf("ExportLibraryArchive failed: %v", err)
	}
	zr, err := zip.OpenReader(zipOut)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	entries := make(map[string]bool)
	for _, f := range zr.File {
		entries[f.Name] = true
	}
	for _, must := range []string{"Power_ICs.kicad_sym", "Power_ICs.pretty/SOT-23-5.kicad_mod", "Power_ICs.3dshapes/SOT23-5.step", "sym-lib-table", "fp-lib-table"} {
		if !entries[must] {
			t.Fatalf("exported archive missing %q (got: %+v)", must, entries)
		}
	}
}

func TestSyncFromKiCadExistingLibraries(t *testing.T) {
	tmp := t.TempDir()
	kicadCfgDir := filepath.Join(tmp, "kicad-cfg")
	thirdPartyDir := filepath.Join(tmp, "3rdparty")
	symDir := filepath.Join(thirdPartyDir, "symbols", "vendor")
	fpDir := filepath.Join(thirdPartyDir, "footprints", "vendor", "Sensors.pretty")
	shapeDir := filepath.Join(thirdPartyDir, "3dmodels", "vendor", "Sensors.3dshapes")
	for _, d := range []string{kicadCfgDir, symDir, fpDir, shapeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	stepPath := filepath.Join(shapeDir, "BMV080.step")
	if err := os.WriteFile(stepPath, []byte("ISO-10303-21;\nEND-ISO-10303-21;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fpContent := `(footprint "BMV080_Outline"
	(layer "F.Cu")
	(attr smd)
	(pad "1" smd rect (at 0 0) (size 0.5 0.5) (layers "F.Cu" "F.Mask"))
	(pad "2" smd rect (at 1 0) (size 0.5 0.5) (layers "F.Cu" "F.Mask"))
	(model "${KICAD9_3RD_PARTY}/3dmodels/vendor/Sensors.3dshapes/BMV080.step" (offset (xyz 0 0 0)) (scale (xyz 1 1 1)) (rotate (xyz 0 0 0)))
)
`
	if err := os.WriteFile(filepath.Join(fpDir, "BMV080_Outline.kicad_mod"), []byte(fpContent), 0o644); err != nil {
		t.Fatal(err)
	}

	symContent := `(kicad_symbol_lib
	(version 20251024)
	(generator "kicad_symbol_editor")
	(symbol "BMV080"
		(in_bom yes)
		(on_board yes)
		(property "Reference" "U" (at 0 2.54 0) (effects (font (size 1.27 1.27))))
		(property "Value" "BMV080" (at 0 -2.54 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "PCM_Sensors:BMV080_Outline" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(property "Description" "Particulate Matter Sensor" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(property "PROD_ID" "CONN-22959" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(symbol "BMV080_1_1"
			(pin input line (at -5.08 0 0) (length 2.54) (name "VCC" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		)
	)
)
`
	symFile := filepath.Join(symDir, "Sensors.kicad_sym")
	if err := os.WriteFile(symFile, []byte(symContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KICAD10_3RD_PARTY", thirdPartyDir)
	t.Setenv("KICAD9_3RD_PARTY", thirdPartyDir)

	if err := os.WriteFile(filepath.Join(kicadCfgDir, "sym-lib-table"), []byte(`(sym_lib_table
	(version 7)
	(lib (name "PCM_Sensors") (type "KiCad") (uri "${KICAD10_3RD_PARTY}/symbols/vendor/Sensors.kicad_sym") (options "") (descr "Third-party sensors"))
)
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kicadCfgDir, "fp-lib-table"), []byte(`(fp_lib_table
	(version 7)
	(lib (name "PCM_Sensors") (type "KiCad") (uri "${KICAD10_3RD_PARTY}/footprints/vendor/Sensors.pretty") (options "") (descr "Third-party sensor footprints"))
)
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		StorageRoot:           filepath.Join(tmp, "storage"),
		KiCadConfigDir:        kicadCfgDir,
		AutoRegisterLibTables: true,
	}
	database, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mgr, err := NewManager(cfg, database, nil)
	if err != nil {
		t.Fatal(err)
	}

	libsSynced, partsSynced, err := mgr.SyncFromKiCad(nil)
	if err != nil {
		t.Fatalf("SyncFromKiCad failed: %v", err)
	}
	if libsSynced != 1 || partsSynced != 1 {
		t.Fatalf("expected 1 lib and 1 part synced, got libs=%d parts=%d", libsSynced, partsSynced)
	}

	// Search by custom metadata property PROD_ID
	found, err := database.SearchParts(db.SearchFilter{Query: "CONN-22959"})
	if err != nil || len(found) != 1 {
		t.Fatalf("expected 1 part matching CONN-22959, got %d (err=%v)", len(found), err)
	}
	if found[0].Footprint == nil || found[0].Footprint.PadCount != 2 {
		t.Fatalf("expected linked footprint with 2 pads, got %+v", found[0].Footprint)
	}
	if len(found[0].Models3D) != 1 || found[0].Models3D[0].VaultPath != stepPath {
		t.Fatalf("expected resolved 3D model path %q, got %+v", stepPath, found[0].Models3D)
	}

	// Verify lazy RawSExpr hydration on GetPart
	full, err := database.GetPart(found[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Symbol == nil || !strings.Contains(full.Symbol.RawSExpr, `"BMV080"`) {
		t.Fatalf("expected lazy-hydrated Symbol.RawSExpr, got %+v", full.Symbol)
	}
	if full.Footprint == nil || !strings.Contains(full.Footprint.RawSExpr, `"BMV080_Outline"`) {
		t.Fatalf("expected lazy-hydrated Footprint.RawSExpr, got %+v", full.Footprint)
	}

	// Move synced part into a custom library and verify files regenerate cleanly
	customLib, err := database.CreateLibrary("MyCustom", "Custom project parts")
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.MovePart(full.ID, customLib.ID); err != nil {
		t.Fatalf("MovePart on synced part failed: %v", err)
	}
	customSymBytes, err := os.ReadFile(filepath.Join(cfg.SymbolsDir(), "MyCustom.kicad_sym"))
	if err != nil || !strings.Contains(string(customSymBytes), `"MyCustom:BMV080_Outline"`) {
		t.Fatalf("expected MyCustom.kicad_sym updated with MyCustom:BMV080_Outline, err=%v", err)
	}

	// Re-running sync should skip unchanged source file
	libsSynced2, partsSynced2, err := mgr.SyncFromKiCad(nil)
	if err != nil {
		t.Fatal(err)
	}
	if libsSynced2 != 0 || partsSynced2 != 0 {
		t.Fatalf("expected unchanged sync to skip, got libs=%d parts=%d", libsSynced2, partsSynced2)
	}
}
