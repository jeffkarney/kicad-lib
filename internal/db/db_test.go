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
