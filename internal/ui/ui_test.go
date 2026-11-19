package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/jeffk/kicad-lib/internal/config"
	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/importer"
	"github.com/jeffk/kicad-lib/internal/kicad"
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

func TestInteractivePreviewWidgetZoomPanAndOrbit(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	w := test.NewWindow(nil)
	defer w.Close()

	fpSExpr := `(footprint "R_0603" (layer "F.Cu") (pad "1" smd roundrect (at -0.8 0) (size 0.9 0.9) (layers "F.Cu" "F.Mask")) (pad "2" smd roundrect (at 0.8 0) (size 0.9 0.9) (layers "F.Cu" "F.Mask")))`
	d2, err := kicad.ParseFootprintDrawing(fpSExpr)
	if err != nil {
		t.Fatal(err)
	}
	pv := NewInteractivePreviewWidget(PreviewMode2D, fyne.NewSize(300, 200))
	pv.Resize(fyne.NewSize(300, 200))
	pv.SetDrawing2D(d2)

	pv.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 1}})
	pv.Dragged(&fyne.DragEvent{Dragged: fyne.Delta{DX: 15, DY: -10}})
	if pv.view2D.Zoom <= 1.0 || pv.view2D.PanX != 15 || pv.view2D.PanY != -10 {
		t.Fatalf("unexpected 2D zoom/pan state: %+v", pv.view2D)
	}
	img2D := pv.renderFrame(300, 200)
	if img2D.Bounds().Dx() != 300 || img2D.Bounds().Dy() != 200 {
		t.Fatalf("unexpected rendered 2D bounds: %+v", img2D.Bounds())
	}

	tmp := t.TempDir()
	wrlPath := filepath.Join(tmp, "box.wrl")
	if err := os.WriteFile(wrlPath, []byte(`#VRML V2.0 utf8
Shape { geometry IndexedFaceSet { coord Coordinate { point [ 0 0 0, 2 0 0, 2 2 0, 0 2 0 ] } coordIndex [ 0, 1, 2, 3, -1 ] } }`), 0o644); err != nil {
		t.Fatal(err)
	}
	mesh, err := kicad.LoadMesh3D(wrlPath)
	if err != nil {
		t.Fatal(err)
	}
	pv3 := NewInteractivePreviewWidget(PreviewMode3D, fyne.NewSize(300, 200))
	pv3.Resize(fyne.NewSize(300, 200))
	pv3.SetMesh3D(mesh)
	startYaw := pv3.cam3D.Yaw
	pv3.Dragged(&fyne.DragEvent{Dragged: fyne.Delta{DX: 20, DY: 10}})
	if pv3.cam3D.Yaw == startYaw {
		t.Fatalf("expected 3D orbit yaw to update on drag")
	}
	pv3.SetDragMode3D("Pan")
	pv3.Dragged(&fyne.DragEvent{Dragged: fyne.Delta{DX: 12, DY: -8}})
	if pv3.cam3D.PanX != 12 || pv3.cam3D.PanY != -8 {
		t.Fatalf("unexpected 3D pan state: %+v", pv3.cam3D)
	}
	pv3.ResetView()
	if pv3.cam3D.Zoom != 1.0 || pv3.cam3D.PanX != 0 {
		t.Fatalf("expected reset camera state, got %+v", pv3.cam3D)
	}
}

func TestDuplicateCompareDialogHeadless(t *testing.T) {
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
	lib, err := database.CreateLibrary("TestLib", "")
	if err != nil {
		t.Fatal(err)
	}
	existing, err := mgr.ImportPart(storage.ImportPartRequest{
		LibraryID: lib.ID,
		PartName:  "NE555",
		Value:     "NE555",
		MPN:       "NE555P",
		Metadata:  map[string]string{"Package": "DIP-8"},
	})
	if err != nil {
		t.Fatal(err)
	}

	a := test.NewApp()
	defer a.Quit()
	w := test.NewWindow(nil)
	defer w.Close()

	cand := &importer.CandidatePart{
		PartName: "NE555",
		Value:    "NE555",
		MPN:      "NE555P",
		Metadata: map[string]string{"Package": "SOIC-8", "Timer": "Precision"},
	}
	ShowDuplicateCompareDialog(w, mgr, lib.ID, cand, []db.Part{*existing}, storage.MergeOptions{Strategy: "merge"}, nil)

	// Also verify staging dialog opens cleanly when duplicate candidates are present
	scanRes := &importer.ScanResult{
		VendorHint: "KiCad Project",
		Candidates: []importer.CandidatePart{*cand},
	}
	ShowDropStagingDialog(w, mgr, scanRes, nil)
}

func TestSyncFromKiCadStatusReset(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		StorageRoot:    filepath.Join(tmp, "storage"),
		KiCadConfigDir: filepath.Join(tmp, "kicad"),
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.KiCadConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}

	symDir := filepath.Join(tmp, "ext_syms")
	if err := os.MkdirAll(symDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symFile := filepath.Join(symDir, "Passive.kicad_sym")
	symContent := `(kicad_symbol_lib (version 20231120) (generator "test")
	(symbol "R_0603" (in_bom yes) (on_board yes)
		(property "Reference" "R" (at 0 0 0) (effects (font (size 1.27 1.27))))
		(property "Value" "10k" (at 0 0 0) (effects (font (size 1.27 1.27))))
		(symbol "R_0603_1_1"
			(pin passive line (at -5.08 0 0) (length 2.54) (name "~" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		)
	)
)`
	if err := os.WriteFile(symFile, []byte(symContent), 0o644); err != nil {
		t.Fatal(err)
	}
	tableContent := `(sym_lib_table (version 7)
	(lib (name "Passive") (type "KiCad") (uri "` + symFile + `") (options "") (descr ""))
)`
	if err := os.WriteFile(filepath.Join(cfg.KiCadConfigDir, "sym-lib-table"), []byte(tableContent), 0o644); err != nil {
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

	// 1. Initial sync puts Passive library into the database
	if _, _, err := mgr.SyncFromKiCad(nil); err != nil {
		t.Fatalf("initial SyncFromKiCad failed: %v", err)
	}

	a := test.NewApp()
	defer a.Quit()
	w := test.NewWindow(nil)
	defer w.Close()

	appInstance := &App{
		fyneApp: a,
		win:     w,
		mgr:     mgr,
	}
	appInstance.buildUI()
	appInstance.refreshData()

	// 2. Run syncFromKiCadAsync when libraries are already up to date (libsSynced == 0)
	appInstance.syncFromKiCadAsync(false)

	// Wait up to 3 seconds for async sync & fyne.Do to complete
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		text := appInstance.statusLbl.Text
		if !strings.HasPrefix(text, "Syncing KiCad libraries") {
			break
		}
	}

	finalStatus := appInstance.statusLbl.Text
	if strings.HasPrefix(finalStatus, "Syncing KiCad libraries") {
		t.Fatalf("expected status label to reset after sync, but still showing: %q", finalStatus)
	}
	if !strings.Contains(finalStatus, "part(s) across") && !strings.Contains(finalStatus, "part(s)") {
		t.Fatalf("expected status label to reflect part/library counts, got: %q", finalStatus)
	}
}
