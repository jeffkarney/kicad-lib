package storage

import (
	"archive/zip"
	"fmt"
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

func TestDuplicateDetectionAndMergePart(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		StorageRoot:    filepath.Join(tmp, "storage"),
		KiCadConfigDir: filepath.Join(tmp, "kicad-cfg"),
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
	lib, err := database.CreateLibrary("MCUs", "Microcontrollers")
	if err != nil {
		t.Fatal(err)
	}

	stepA := filepath.Join(tmp, "a.step")
	stepB := filepath.Join(tmp, "b.step")
	_ = os.WriteFile(stepA, []byte("ISO-10303-21;\nEND-ISO-10303-21;\n"), 0o644)
	_ = os.WriteFile(stepB, []byte("ISO-10303-21;\nEND-ISO-10303-21;\n"), 0o644)

	symV1 := `(symbol "STM32F405RGT6"
		(in_bom yes)
		(on_board yes)
		(property "Reference" "U" (at 0 2.54 0) (effects (font (size 1.27 1.27))))
		(property "Value" "STM32F405RGT6" (at 0 -2.54 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(symbol "STM32F405RGT6_1_1"
			(pin input line (at -5.08 0 0) (length 2.54) (name "VDD" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		)
	)`
	fpNew := `(footprint "LQFP-64"
		(layer "F.Cu")
		(attr smd)
		(pad "1" smd rect (at -1 0) (size 0.5 0.5) (layers "F.Cu" "F.Mask"))
	)`

	orig, err := mgr.ImportPart(ImportPartRequest{
		LibraryID:    lib.ID,
		PartName:     "STM32F405RGT6",
		Value:        "STM32F405RGT6",
		Description:  "ARM Cortex-M4 MCU",
		MPN:          "STM32F405RGT6",
		Manufacturer: "STMicroelectronics",
		SourceType:   "drop",
		SymbolName:   "STM32F405RGT6",
		SymbolSExpr:  symV1,
		Models3D:     []ImportModel3D{{SourcePath: stepA, FileName: "a.step", Format: "step"}},
		Metadata:     map[string]string{"Voltage": "3.3V", "Package": "LQFP-64"},
	})
	if err != nil {
		t.Fatalf("initial ImportPart failed: %v", err)
	}

	// Duplicate detection by exact name & by MPN
	dupsByName, err := database.FindDuplicateParts("stm32f405rgt6", "", "", lib.ID)
	if err != nil || len(dupsByName) != 1 || dupsByName[0].ID != orig.ID {
		t.Fatalf("expected duplicate match by name, got %+v (err=%v)", dupsByName, err)
	}
	dupsByMPN, err := database.FindDuplicateParts("AliasPart", "STM32F405RGT6", "", lib.ID)
	if err != nil || len(dupsByMPN) != 1 || dupsByMPN[0].ID != orig.ID {
		t.Fatalf("expected duplicate match by MPN, got %+v (err=%v)", dupsByMPN, err)
	}
	if dupsByMPN[0].Symbol == nil || dupsByMPN[0].Metadata["Voltage"] != "3.3V" {
		t.Fatalf("expected hydrated relations on duplicate match: %+v", dupsByMPN[0])
	}

	incReq := ImportPartRequest{
		LibraryID:      lib.ID,
		PartName:       "STM32F405RGT6",
		Description:    "High-performance MCU",
		LCSCPart:       "C15742",
		SourceType:     "drop",
		FootprintName:  "LQFP-64",
		FootprintSExpr: fpNew,
		Models3D:       []ImportModel3D{{SourcePath: stepB, FileName: "b.step", Format: "step"}},
		Metadata:       map[string]string{"Package": "LQFP-64-EP", "Temp": "-40..85C"},
	}

	// 1. Skip strategy leaves original unchanged
	skipped, err := mgr.MergePart(orig.ID, incReq, MergeOptions{Strategy: "skip"})
	if err != nil || skipped.Footprint != nil {
		t.Fatalf("expected skip strategy to leave part unchanged, err=%v", err)
	}

	// 2. Selective merge: keep existing symbol, take incoming footprint, combine 3D models, prefer incoming metadata
	merged, err := mgr.MergePart(orig.ID, incReq, MergeOptions{
		Strategy:             "merge",
		UseIncomingDetails:   true,
		UseIncomingSymbol:    false,
		UseIncomingFootprint: true,
		Model3DMode:          "combine",
		MetadataMode:         "prefer_incoming",
	})
	if err != nil {
		t.Fatalf("MergePart failed: %v", err)
	}
	if merged.Symbol == nil || merged.Symbol.PinCount != 1 {
		t.Fatalf("expected existing symbol retained on merge, got %+v", merged.Symbol)
	}
	if merged.Footprint == nil || merged.Footprint.Name != "LQFP-64" {
		t.Fatalf("expected incoming footprint added on merge, got %+v", merged.Footprint)
	}
	if len(merged.Models3D) != 2 {
		t.Fatalf("expected 2 combined 3D models (a.step, b.step), got %d", len(merged.Models3D))
	}
	if merged.Metadata["Voltage"] != "3.3V" || merged.Metadata["Package"] != "LQFP-64-EP" || merged.Metadata["Temp"] != "-40..85C" {
		t.Fatalf("unexpected merged metadata: %+v", merged.Metadata)
	}
	if merged.LCSCPart != "C15742" {
		t.Fatalf("expected incoming LCSC ID merged, got %q", merged.LCSCPart)
	}

	// 3. Rename strategy creates a distinct new part in the library
	renamed, err := mgr.MergePart(orig.ID, incReq, MergeOptions{
		Strategy: "rename",
	})
	if err != nil {
		t.Fatalf("MergePart rename failed: %v", err)
	}
	if renamed.ID == orig.ID || renamed.Name != "STM32F405RGT6_2" {
		t.Fatalf("expected renamed copy STM32F405RGT6_2, got id=%d name=%q", renamed.ID, renamed.Name)
	}
}

func TestSyncFromKiCadDirectorySymbolLibraries(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		StorageRoot:    filepath.Join(tmp, "storage"),
		KiCadConfigDir: filepath.Join(tmp, "kicad-cfg"),
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

	// 1. Create a .kicad_symdir directory library
	symDir := filepath.Join(tmp, "Custom_Switches.kicad_symdir")
	if err := os.MkdirAll(symDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sym1 := `(kicad_symbol_lib (version 20231120) (generator "kicad_symbol_editor")
		(symbol "SW_1" (property "Reference" "SW" (at 0 0 0)) (property "Value" "SW_1" (at 0 2 0)))
	)`
	sym2 := `(kicad_symbol_lib (version 20231120) (generator "kicad_symbol_editor")
		(symbol "SW_2" (property "Reference" "SW" (at 0 0 0)) (property "Value" "SW_2" (at 0 2 0)))
	)`
	if err := os.WriteFile(filepath.Join(symDir, "SW_1.kicad_sym"), []byte(sym1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symDir, "SW_2.kicad_sym"), []byte(sym2), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. Create a component directory library with .kicad_sym, footprints.pretty, and .step
	cmpDir := filepath.Join(tmp, "ESP32_Module")
	cmpFPDir := filepath.Join(cmpDir, "footprints.pretty")
	if err := os.MkdirAll(cmpFPDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmpSym := `(kicad_symbol_lib (version 20231120) (generator "kicad_symbol_editor")
		(symbol "ESP32_DEV"
			(property "Reference" "U" (at 0 0 0))
			(property "Value" "ESP32_DEV" (at 0 2 0))
			(property "Footprint" "ESP32_DEV_FP" (at 0 -2 0))
		)
	)`
	cmpFP := `(footprint "ESP32_DEV_FP" (layer "F.Cu") (descr "ESP32 Dev Module")
		(pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu"))
	)`
	if err := os.WriteFile(filepath.Join(cmpDir, "ESP32_DEV.kicad_sym"), []byte(cmpSym), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmpFPDir, "ESP32_DEV_FP.kicad_mod"), []byte(cmpFP), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmpDir, "ESP32_DEV.step"), []byte("ISO-10303-21;\nEND-ISO-10303-21;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	symTable := fmt.Sprintf(`(sym_lib_table
	(version 7)
	(lib (name "Custom_Switches") (type "KiCad") (uri "%s") (options "") (descr "Switches symdir"))
	(lib (name "ESP32_Module") (type "KiCad") (uri "%s") (options "") (descr "ESP32 component dir"))
)
`, symDir, cmpDir)
	if err := os.MkdirAll(cfg.KiCadConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.KiCadConfigDir, "sym-lib-table"), []byte(symTable), 0o644); err != nil {
		t.Fatal(err)
	}

	libsSynced, partsSynced, err := mgr.SyncFromKiCad(nil)
	if err != nil {
		t.Fatalf("SyncFromKiCad failed: %v", err)
	}
	if libsSynced != 2 {
		t.Fatalf("expected 2 libraries synced, got %d", libsSynced)
	}
	if partsSynced != 3 {
		t.Fatalf("expected 3 parts synced (2 switches, 1 esp32), got %d", partsSynced)
	}

	swLib, err := database.GetLibraryByName("Custom_Switches")
	if err != nil {
		t.Fatalf("GetLibraryByName(Custom_Switches) failed: %v", err)
	}
	swParts, err := database.ListPartsByLibrary(swLib.ID)
	if err != nil || len(swParts) != 2 {
		t.Fatalf("expected 2 switch parts, got %d (err: %v)", len(swParts), err)
	}
	for _, p := range swParts {
		if p.Symbol == nil || !strings.HasSuffix(p.Symbol.VaultPath, ".kicad_sym") {
			t.Fatalf("expected part symbol vault path to be .kicad_sym file, got %+v", p.Symbol)
		}
	}

	espLib, err := database.GetLibraryByName("ESP32_Module")
	if err != nil {
		t.Fatalf("GetLibraryByName(ESP32_Module) failed: %v", err)
	}
	espParts, err := database.ListPartsByLibrary(espLib.ID)
	if err != nil || len(espParts) != 1 {
		t.Fatalf("expected 1 ESP32 part, got %d (err: %v)", len(espParts), err)
	}
	p := espParts[0]
	if p.Footprint == nil || p.Footprint.Name != "ESP32_DEV_FP" {
		t.Fatalf("expected auto-discovered footprint ESP32_DEV_FP, got %+v", p.Footprint)
	}
	if len(p.Models3D) != 1 || p.Models3D[0].FileName != "ESP32_DEV.step" {
		t.Fatalf("expected auto-discovered 3D model ESP32_DEV.step, got %+v", p.Models3D)
	}
}

func TestSyncFromKiCadStandaloneFootprintLibrary(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		StorageRoot:    filepath.Join(tmp, "storage"),
		KiCadConfigDir: filepath.Join(tmp, "kicad-cfg"),
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

	fpDir := filepath.Join(tmp, "Custom_Footprints.pretty")
	if err := os.MkdirAll(fpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fp1 := `(footprint "SOT-23-6_Custom" (layer "F.Cu") (descr "6-pin SOT23") (tags "sot23 smd")
		(pad "1" smd rect (at 0 0) (size 0.5 0.5) (layers "F.Cu"))
		(pad "2" smd rect (at 1 0) (size 0.5 0.5) (layers "F.Cu"))
	)`
	if err := os.WriteFile(filepath.Join(fpDir, "SOT-23-6_Custom.kicad_mod"), []byte(fp1), 0o644); err != nil {
		t.Fatal(err)
	}

	fpTable := fmt.Sprintf(`(fp_lib_table
	(version 7)
	(lib (name "Custom_Footprints") (type "KiCad") (uri "%s") (options "") (descr "Standalone footprints"))
)
`, fpDir)
	if err := os.MkdirAll(cfg.KiCadConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.KiCadConfigDir, "fp-lib-table"), []byte(fpTable), 0o644); err != nil {
		t.Fatal(err)
	}

	libsSynced, partsSynced, err := mgr.SyncFromKiCad(nil)
	if err != nil {
		t.Fatalf("SyncFromKiCad failed: %v", err)
	}
	if libsSynced != 1 {
		t.Fatalf("expected 1 library synced, got %d", libsSynced)
	}
	if partsSynced != 1 {
		t.Fatalf("expected 1 part synced, got %d", partsSynced)
	}

	lib, err := database.GetLibraryByName("Custom_Footprints")
	if err != nil {
		t.Fatalf("GetLibraryByName(Custom_Footprints) failed: %v", err)
	}
	if lib.FPSourceURI != fpDir {
		t.Fatalf("expected FPSourceURI=%q, got %q", fpDir, lib.FPSourceURI)
	}

	parts, err := database.ListPartsByLibrary(lib.ID)
	if err != nil || len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d (err: %v)", len(parts), err)
	}
	pt := parts[0]
	if pt.Name != "SOT-23-6_Custom" || pt.Footprint == nil || pt.Footprint.PadCount != 2 {
		t.Fatalf("unexpected footprint part: %+v", pt)
	}
	if pt.Description != "6-pin SOT23" {
		t.Fatalf("expected description '6-pin SOT23', got %q", pt.Description)
	}

	libs2, parts2, err := mgr.SyncFromKiCad(nil)
	if err != nil {
		t.Fatalf("second SyncFromKiCad failed: %v", err)
	}
	if libs2 != 0 || parts2 != 0 {
		t.Fatalf("expected 0 synced on second run, got libs=%d parts=%d", libs2, parts2)
	}
}

func TestLiveKiCadSync(t *testing.T) {
	if os.Getenv("KICAD_LIVE_TEST") != "1" {
		t.Skip("skipping live sync test against user database (set KICAD_LIVE_TEST=1 to enable)")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skip("skipping live sync test: no config")
	}
	if _, err := os.Stat(cfg.KiCadConfigDir); err != nil {
		t.Skip("skipping live sync test: kicad config dir missing")
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

	libsSynced, partsSynced, err := mgr.SyncFromKiCad(func(p SyncProgress) {
		t.Logf("[%d/%d] (%s) parts synced so far: %d", p.Current, p.Total, p.LibraryName, p.PartsSynced)
	})
	if err != nil {
		t.Fatalf("live SyncFromKiCad failed: %v", err)
	}
	t.Logf("Live sync completed: %d libs synced, %d parts synced", libsSynced, partsSynced)

	// Check specifically that the previously missing libraries are now present
	expectedLibs := []string{
		"git-switch",
		"git-connector-generic",
		"RP2354",
		"LDL212",
		"BD33H 3.3V reg",
		"ESP32-H2-MINI-1U-H2S",
		"esp32-mini-1u",
		"KiCad Symbols - Git",
		"Regulator 3.3v NCV8187AMT330TAG",
		"Library",
		"Enclosure PCBs",
	}
	for _, name := range expectedLibs {
		lib, err := database.GetLibraryByName(name)
		if err != nil {
			t.Errorf("expected library %q to be in database after sync, err=%v", name, err)
			continue
		}
		parts, _ := database.ListPartsByLibrary(lib.ID)
		t.Logf("Verified library in DB: %s (id: %d, parts: %d, source: %s, fp: %s)",
			lib.Name, lib.ID, len(parts), filepath.Base(lib.SourceURI), filepath.Base(lib.FPSourceURI))
	}
}

func TestSetLibraryKiCadEnabledAndRegeneratePreservesDisabled(t *testing.T) {
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

	// 1. Managed library
	lib, err := database.CreateLibrary("Custom_LEDs", "LED library")
	if err != nil {
		t.Fatal(err)
	}
	symSExpr := `(symbol "LED_Red"
		(in_bom yes)
		(on_board yes)
		(property "Reference" "D" (at 0 2.54 0) (effects (font (size 1.27 1.27))))
		(property "Value" "LED_Red" (at 0 -2.54 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(symbol "LED_Red_1_1"
			(pin passive line (at -5.08 0 0) (length 2.54) (name "K" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		)
	)`
	if _, err := mgr.ImportPart(ImportPartRequest{
		LibraryID:   lib.ID,
		PartName:    "LED_Red",
		SymbolName:  "LED_Red",
		SymbolSExpr: symSExpr,
	}); err != nil {
		t.Fatalf("ImportPart failed: %v", err)
	}

	// Initially enabled in both tables
	syms, fps, _, err := kicad.LoadEffectiveLibTables(cfg.KiCadConfigDir, "", nil, false)
	if err != nil || len(syms) != 1 || syms[0].Disabled || len(fps) != 1 || fps[0].Disabled {
		t.Fatalf("expected Custom_LEDs initially enabled, syms=%+v fps=%+v err=%v", syms, fps, err)
	}

	// Disable in KiCad
	if err := mgr.SetLibraryKiCadEnabled(lib.ID, false); err != nil {
		t.Fatalf("SetLibraryKiCadEnabled(false) failed: %v", err)
	}
	gotLib, err := database.GetLibrary(lib.ID)
	if err != nil || gotLib.KiCadEnabled {
		t.Fatalf("expected DB KiCadEnabled=false, got %+v (err=%v)", gotLib, err)
	}
	syms, fps, _, err = kicad.LoadEffectiveLibTables(cfg.KiCadConfigDir, "", nil, false)
	if err != nil || len(syms) != 1 || !syms[0].Disabled || len(fps) != 1 || !fps[0].Disabled {
		t.Fatalf("expected Custom_LEDs disabled in KiCad tables, syms=%+v fps=%+v err=%v", syms, fps, err)
	}

	// Regenerating while disabled should preserve (disabled)
	if err := mgr.RegenerateLibrary(lib.ID); err != nil {
		t.Fatalf("RegenerateLibrary failed: %v", err)
	}
	syms, fps, _, err = kicad.LoadEffectiveLibTables(cfg.KiCadConfigDir, "", nil, false)
	if err != nil || len(syms) != 1 || !syms[0].Disabled || len(fps) != 1 || !fps[0].Disabled {
		t.Fatalf("expected RegenerateLibrary to preserve disabled state, syms=%+v fps=%+v err=%v", syms, fps, err)
	}

	// Re-enable in KiCad
	if err := mgr.SetLibraryKiCadEnabled(lib.ID, true); err != nil {
		t.Fatalf("SetLibraryKiCadEnabled(true) failed: %v", err)
	}
	syms, fps, _, err = kicad.LoadEffectiveLibTables(cfg.KiCadConfigDir, "", nil, false)
	if err != nil || len(syms) != 1 || syms[0].Disabled || len(fps) != 1 || fps[0].Disabled {
		t.Fatalf("expected Custom_LEDs re-enabled in KiCad tables, syms=%+v fps=%+v err=%v", syms, fps, err)
	}

	// 2. External kicad_sync library preserves existing ${VAR} URI when toggled
	thirdPartyDir := filepath.Join(tmp, "3rdparty")
	symExtDir := filepath.Join(thirdPartyDir, "symbols")
	fpExtDir := filepath.Join(thirdPartyDir, "footprints", "Ext.pretty")
	if err := os.MkdirAll(symExtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fpExtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KICAD10_3RD_PARTY", thirdPartyDir)
	if err := os.WriteFile(filepath.Join(symExtDir, "Ext.kicad_sym"), []byte(`(kicad_symbol_lib (version 20231120) (generator "test")
		(symbol "ExtPart" (property "Reference" "U" (at 0 0 0)) (property "Value" "ExtPart" (at 0 2 0)) (property "Footprint" "Ext:ExtFP" (at 0 0 0)))
	)`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fpExtDir, "ExtFP.kicad_mod"), []byte(`(footprint "ExtFP" (layer "F.Cu") (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")))`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := kicad.RegisterSymbolLibrary(cfg.KiCadConfigDir, "Ext", "${KICAD10_3RD_PARTY}/symbols/Ext.kicad_sym", "Ext syms"); err != nil {
		t.Fatal(err)
	}
	if err := kicad.RegisterFootprintLibrary(cfg.KiCadConfigDir, "Ext", "${KICAD10_3RD_PARTY}/footprints/Ext.pretty", "Ext fps"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.SyncFromKiCad(nil); err != nil {
		t.Fatalf("SyncFromKiCad failed: %v", err)
	}
	extLib, err := database.GetLibraryByName("Ext")
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetLibraryKiCadEnabled(extLib.ID, false); err != nil {
		t.Fatalf("SetLibraryKiCadEnabled on external lib failed: %v", err)
	}
	rawSymTbl, err := os.ReadFile(filepath.Join(cfg.KiCadConfigDir, "sym-lib-table"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawSymTbl), "${KICAD10_3RD_PARTY}/symbols/Ext.kicad_sym") || !strings.Contains(string(rawSymTbl), "(disabled)") {
		t.Fatalf("expected preserved ${KICAD10_3RD_PARTY} URI and (disabled) on Ext, got:\n%s", string(rawSymTbl))
	}

	// 3. Batch-toggle 200 synced libraries in a single pass
	var batchIDs []int64
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("Bulk_Ext_%03d", i)
		parts := []db.Part{
			{Name: fmt.Sprintf("P_%d_1", i), SourceType: "kicad_sync"},
			{Name: fmt.Sprintf("P_%d_2", i), SourceType: "kicad_sync"},
		}
		l, _, berr := database.BulkSyncLibrary(name, "Bulk test", filepath.Join(symExtDir, name+".kicad_sym"), "", 100, parts)
		if berr != nil {
			t.Fatalf("BulkSyncLibrary %s failed: %v", name, berr)
		}
		batchIDs = append(batchIDs, l.ID)
	}
	if err := mgr.SetLibrariesKiCadEnabled(batchIDs, false); err != nil {
		t.Fatalf("batch SetLibrariesKiCadEnabled(false) failed: %v", err)
	}
	if err := mgr.SetLibrariesKiCadEnabled(batchIDs, true); err != nil {
		t.Fatalf("batch SetLibrariesKiCadEnabled(true) failed: %v", err)
	}
}


