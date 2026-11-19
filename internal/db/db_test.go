package db

import (
	"path/filepath"
	"testing"
)

func TestDBLifecycleAndFTS5Search(t *testing.T) {
	tmp := t.TempDir()
	database, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	lib1, err := database.CreateLibrary("MCU_Custom", "Microcontrollers")
	if err != nil {
		t.Fatalf("CreateLibrary failed: %v", err)
	}
	lib2, err := database.CreateLibrary("Power_Regulators", "Voltage regulators")
	if err != nil {
		t.Fatalf("CreateLibrary 2 failed: %v", err)
	}

	p := &Part{
		LibraryID:    lib1.ID,
		Name:         "STM32G431CBU6",
		Value:        "STM32G431CBU6",
		Description:  "ARM Cortex-M4 170MHz MCU",
		Keywords:     "arm stm32 mcu",
		MPN:          "STM32G431CBU6",
		Manufacturer: "STMicroelectronics",
		LCSCPart:     "C529315",
		SourceType:   "drop",
		Symbol: &Symbol{
			Name:     "STM32G431CBU6",
			RawSExpr: `(symbol "STM32G431CBU6")`,
			PinCount: 48,
		},
		Footprint: &Footprint{
			Name:     "UFQFPN-48_7x7mm_P0.5mm",
			RawSExpr: `(footprint "UFQFPN-48_7x7mm_P0.5mm")`,
			PadCount: 49,
		},
		Models3D: []Model3D{
			{FileName: "UFQFPN-48.step", Format: "step", VaultPath: "vault/UFQFPN-48.step"},
		},
		Metadata: map[string]string{
			"Package": "UFQFPN-48",
			"Voltage": "3.3V",
			"Core":    "Cortex-M4",
		},
	}

	if err := database.SavePart(p); err != nil {
		t.Fatalf("SavePart failed: %v", err)
	}

	// FTS5 search by metadata value
	results, err := database.SearchParts(SearchFilter{Query: "Cortex-M4"})
	if err != nil {
		t.Fatalf("SearchParts FTS5 failed: %v", err)
	}
	if len(results) != 1 || results[0].Name != "STM32G431CBU6" {
		t.Fatalf("unexpected FTS5 results: %+v", results)
	}

	// Metadata key-value filter + hyphenated FTS5 query
	has3D := true
	results, err = database.SearchParts(SearchFilter{
		Query:           "UFQFPN-48",
		MetadataFilters: map[string]string{"Voltage": "3.3"},
		Has3DModel:      &has3D,
	})
	if err != nil {
		t.Fatalf("SearchParts combined filter failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}

	// Move part to lib2
	if err := database.MovePart(p.ID, lib2.ID); err != nil {
		t.Fatalf("MovePart failed: %v", err)
	}
	got, err := database.GetPart(p.ID)
	if err != nil {
		t.Fatalf("GetPart failed: %v", err)
	}
	if got.LibraryID != lib2.ID || got.LibraryName != "Power_Regulators" {
		t.Fatalf("expected part moved to lib2, got library %d (%s)", got.LibraryID, got.LibraryName)
	}

	// Update metadata and verify FTS5 updates
	if err := database.UpdatePartMetadata(p.ID, map[string]string{"Package": "UFQFPN-48", "Status": "Preferred"}); err != nil {
		t.Fatalf("UpdatePartMetadata failed: %v", err)
	}
	results, err = database.SearchParts(SearchFilter{Query: "Preferred"})
	if err != nil {
		t.Fatalf("SearchParts after metadata update failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for Preferred, got %d", len(results))
	}
}

func TestLibraryVisibilityAndKiCadEnabled(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "vis.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	libA, err := database.CreateLibrary("LibA", "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	libB, err := database.CreateLibrary("LibB", "Beta")
	if err != nil {
		t.Fatal(err)
	}

	if !libA.Hidden || !libA.KiCadEnabled {
		t.Fatalf("expected new library to default to Hidden=true, KiCadEnabled=true, got %+v", libA)
	}
	if !libB.Hidden || !libB.KiCadEnabled {
		t.Fatalf("expected new library B to default to Hidden=true, KiCadEnabled=true, got %+v", libB)
	}

	if err := database.SavePart(&Part{LibraryID: libA.ID, Name: "PartA"}); err != nil {
		t.Fatal(err)
	}
	if err := database.SavePart(&Part{LibraryID: libB.ID, Name: "PartB"}); err != nil {
		t.Fatal(err)
	}

	// ExcludeHidden=true should return 0 parts when all libraries are hidden
	hiddenRes, err := database.SearchParts(SearchFilter{ExcludeHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hiddenRes) != 0 {
		t.Fatalf("expected 0 parts when all libraries hidden, got %d", len(hiddenRes))
	}

	// ExcludeHidden=false still finds all parts
	allRes, err := database.SearchParts(SearchFilter{ExcludeHidden: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(allRes) != 2 {
		t.Fatalf("expected 2 parts with ExcludeHidden=false, got %d", len(allRes))
	}

	// Un-hide LibA individually
	if err := database.SetLibraryHidden(libA.ID, false); err != nil {
		t.Fatal(err)
	}
	gotA, err := database.GetLibrary(libA.ID)
	if err != nil || gotA.Hidden {
		t.Fatalf("expected LibA Hidden=false, got %+v (err=%v)", gotA, err)
	}
	visRes, err := database.SearchParts(SearchFilter{ExcludeHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(visRes) != 1 || visRes[0].Name != "PartA" {
		t.Fatalf("expected only PartA when LibA is shown, got %+v", visRes)
	}

	// Bulk un-hide & KiCad enable/disable
	if err := database.SetLibrariesHidden([]int64{libA.ID, libB.ID}, false); err != nil {
		t.Fatal(err)
	}
	visRes2, err := database.SearchParts(SearchFilter{ExcludeHidden: true})
	if err != nil || len(visRes2) != 2 {
		t.Fatalf("expected 2 visible parts after bulk un-hide, got %d (err=%v)", len(visRes2), err)
	}

	if err := database.SetLibraryKiCadEnabled(libA.ID, false); err != nil {
		t.Fatal(err)
	}
	gotA, _ = database.GetLibrary(libA.ID)
	if gotA.KiCadEnabled {
		t.Fatalf("expected LibA KiCadEnabled=false")
	}
	if err := database.SetLibrariesKiCadEnabled([]int64{libA.ID, libB.ID}, false); err != nil {
		t.Fatal(err)
	}
	libs, err := database.ListLibraries()
	if err != nil || len(libs) != 2 {
		t.Fatalf("ListLibraries failed: %v", err)
	}
	for _, l := range libs {
		if l.KiCadEnabled {
			t.Fatalf("expected %s KiCadEnabled=false after bulk disable", l.Name)
		}
	}

	// Verify idempotent migration from legacy schema without hidden/kicad_enabled columns
	_ = database.Close()
	legacyPath := filepath.Join(tmp, "legacy.db")
	legacyDB, err := Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.conn.Exec(`DROP TABLE libraries; CREATE TABLE libraries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		description TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	INSERT INTO libraries (name, description, created_at, updated_at) VALUES ('LegacyLib', 'old', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);`); err != nil {
		t.Fatal(err)
	}
	_ = legacyDB.Close()

	reopened, err := Open(legacyPath)
	if err != nil {
		t.Fatalf("reopen legacy DB failed: %v", err)
	}
	defer reopened.Close()
	leg, err := reopened.GetLibraryByName("LegacyLib")
	if err != nil {
		t.Fatalf("GetLibraryByName(LegacyLib) failed: %v", err)
	}
	if !leg.Hidden || !leg.KiCadEnabled {
		t.Fatalf("expected migrated LegacyLib to default to Hidden=true and KiCadEnabled=true, got %+v", leg)
	}
}
