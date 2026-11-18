package ui

import (
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/jeffk/kicad-lib/internal/config"
	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/storage"
)

func TestPartInspectorHeadless(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		StorageRoot:    filepath.Join(tmp, "storage"),
		KiCadConfigDir: filepath.Join(tmp, "kicad"),
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mgr, err := storage.NewManager(cfg, database, nil)
	if err != nil {
		t.Fatal(err)
	}

	lib, err := database.CreateLibrary("TestLib", "Test Library")
	if err != nil {
		t.Fatal(err)
	}
	part, err := mgr.ImportPart(storage.ImportPartRequest{
		LibraryID:    lib.ID,
		PartName:     "R_10K",
		Value:        "10k",
		Description:  "10k Ohm Resistor",
		MPN:          "RC0603FR-0710KL",
		Manufacturer: "Yageo",
		Metadata:     map[string]string{"Package": "0603", "Tolerance": "1%"},
	})
	if err != nil {
		t.Fatal(err)
	}

	a := test.NewApp()
	defer a.Quit()
	w := test.NewWindow(nil)
	defer w.Close()

	insp := NewPartInspector(w, mgr, nil)
	insp.SetLibraries([]db.Library{*lib})
	insp.SetPart(part)

	if insp.titleLbl.Text != "R_10K" {
		t.Fatalf("expected title R_10K, got %q", insp.titleLbl.Text)
	}
	if insp.mpnEntry.Text != "RC0603FR-0710KL" {
		t.Fatalf("expected MPN entry populated, got %q", insp.mpnEntry.Text)
	}
	if insp.editMeta["Tolerance"] != "1%" {
		t.Fatalf("expected Tolerance=1%% in inspector metadata, got %+v", insp.editMeta)
	}

	// Add a metadata field via the UI entries and save
	insp.newKeyEntry.SetText("Power")
	insp.newValEntry.SetText("0.1W")
	insp.handleAddMetadataField()
	insp.handleSaveMetadata()

	updated, err := database.GetPart(part.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Metadata["Power"] != "0.1W" {
		t.Fatalf("expected Power=0.1W saved to DB, got %+v", updated.Metadata)
	}
}
