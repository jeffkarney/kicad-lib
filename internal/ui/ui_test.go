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

func TestLibraryVisibilitySearchAndKiCadToggleHeadless(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		StorageRoot:           filepath.Join(tmp, "storage"),
		KiCadConfigDir:        filepath.Join(tmp, "kicad"),
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

	mgr, err := storage.NewManager(cfg, database, nil)
	if err != nil {
		t.Fatal(err)
	}

	libAlpha, err := database.CreateLibrary("Alpha_MCU", "Microcontrollers")
	if err != nil {
		t.Fatal(err)
	}
	libBeta, err := database.CreateLibrary("Beta_Sensors", "Environmental sensors")
	if err != nil {
		t.Fatal(err)
	}
	libGamma, err := database.CreateLibrary("Gamma_Power", "Power regulators")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.ImportPart(storage.ImportPartRequest{LibraryID: libAlpha.ID, PartName: "AlphaPart"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ImportPart(storage.ImportPartRequest{LibraryID: libBeta.ID, PartName: "BetaPart"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ImportPart(storage.ImportPartRequest{LibraryID: libGamma.ID, PartName: "GammaPart"}); err != nil {
		t.Fatal(err)
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

	// 1. All libraries hidden by default
	if len(appInstance.allLibraries) != 3 {
		t.Fatalf("expected 3 total libraries, got %d", len(appInstance.allLibraries))
	}
	if len(appInstance.visibleLibraries) != 0 || len(appInstance.libraries) != 0 {
		t.Fatalf("expected 0 visible libraries by default, got visible=%d shown=%d", len(appInstance.visibleLibraries), len(appInstance.libraries))
	}
	if len(appInstance.parts) != 0 {
		t.Fatalf("expected 0 parts shown when all libraries are hidden, got %d", len(appInstance.parts))
	}
	if !strings.Contains(appInstance.statusLbl.Text, "3 hidden") {
		t.Fatalf("expected status label to report 3 hidden libraries, got %q", appInstance.statusLbl.Text)
	}

	// 2. Manage Libraries view: search, un-hide, status filter, and KiCad enable/disable
	lmv := newLibraryManagerView(w, mgr, appInstance.refreshData)
	if len(lmv.filteredLibs) != 3 {
		t.Fatalf("expected 3 libraries in manager view, got %d", len(lmv.filteredLibs))
	}
	lmv.searchEntry.SetText("Beta")
	if len(lmv.filteredLibs) != 1 || lmv.filteredLibs[0].Name != "Beta_Sensors" {
		t.Fatalf("expected Beta_Sensors when filtering by Beta, got %+v", lmv.filteredLibs)
	}
	lmv.setFilteredHidden(false)
	if len(appInstance.visibleLibraries) != 1 || appInstance.visibleLibraries[0].Name != "Beta_Sensors" {
		t.Fatalf("expected Beta_Sensors un-hidden in main app, got %+v", appInstance.visibleLibraries)
	}
	if len(appInstance.parts) != 1 || appInstance.parts[0].Name != "BetaPart" {
		t.Fatalf("expected only BetaPart shown in main app, got %+v", appInstance.parts)
	}

	lmv.searchEntry.SetText("")
	lmv.statusSelect.SetSelected(libFilterShown)
	if len(lmv.filteredLibs) != 1 || lmv.filteredLibs[0].Name != "Beta_Sensors" {
		t.Fatalf("expected Shown status filter to match Beta_Sensors, got %+v", lmv.filteredLibs)
	}
	lmv.setFilteredKiCadEnabled(false)
	if lmv.filteredLibs[0].KiCadEnabled {
		t.Fatalf("expected Beta_Sensors KiCadEnabled=false after disable")
	}
	syms, _, _, err := kicad.LoadEffectiveLibTables(cfg.KiCadConfigDir, "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	foundDisabledBeta := false
	for _, s := range syms {
		if s.Name == "Beta_Sensors" && s.Disabled {
			foundDisabledBeta = true
		}
	}
	if !foundDisabledBeta {
		t.Fatalf("expected Beta_Sensors marked disabled in sym-lib-table, got %+v", syms)
	}

	// 3. Un-hide Alpha_MCU too and verify sidebar search filter + quick hide/KiCad toggle
	if err := database.SetLibraryHidden(libAlpha.ID, false); err != nil {
		t.Fatal(err)
	}
	appInstance.refreshData()
	if len(appInstance.visibleLibraries) != 2 {
		t.Fatalf("expected 2 visible libraries, got %d", len(appInstance.visibleLibraries))
	}
	appInstance.libSearchEntry.SetText("Alpha")
	if len(appInstance.libraries) != 1 || appInstance.libraries[0].Name != "Alpha_MCU" {
		t.Fatalf("expected sidebar filter Alpha to narrow to Alpha_MCU, got %+v", appInstance.libraries)
	}
	appInstance.libList.Select(1)
	if appInstance.selectedLibID != libAlpha.ID {
		t.Fatalf("expected selectedLibID=%d, got %d", libAlpha.ID, appInstance.selectedLibID)
	}
	appInstance.handleToggleSelectedLibraryKiCad()
	gotAlpha, _ := database.GetLibrary(libAlpha.ID)
	if gotAlpha.KiCadEnabled {
		t.Fatalf("expected quick toggle to disable Alpha_MCU in KiCad")
	}
	appInstance.handleHideSelectedLibrary()
	if appInstance.selectedLibID != 0 {
		t.Fatalf("expected selectedLibID reset to 0 after hiding library")
	}
	if len(appInstance.visibleLibraries) != 1 {
		t.Fatalf("expected 1 visible library after hiding Alpha_MCU, got %d", len(appInstance.visibleLibraries))
	}

	// 4. PartInspector move target library search filter
	alphaPart, err := database.GetPart(appInstance.parts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	appInstance.inspector.SetPart(alphaPart)
	appInstance.inspector.moveFilterEntry.SetText("Gamma")
	if len(appInstance.inspector.moveSelect.Options) != 1 || appInstance.inspector.moveSelect.Options[0] != "Gamma_Power" {
		t.Fatalf("expected PartInspector move filter to narrow to Gamma_Power, got %+v", appInstance.inspector.moveSelect.Options)
	}

	// 5. Staging dialog opens cleanly with target library filter
	scanRes := &importer.ScanResult{
		VendorHint: "Test",
		Candidates: []importer.CandidatePart{{PartName: "NewPart", Value: "1k"}},
	}
	ShowDropStagingDialog(w, mgr, scanRes, nil)
}

func TestDropStagingDialogBatchIntraDuplicates(t *testing.T) {
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

	mgr, err := storage.NewManager(cfg, database, nil)
	if err != nil {
		t.Fatal(err)
	}

	lib, err := database.CreateLibrary("Jeff K", "Test Lib")
	if err != nil {
		t.Fatal(err)
	}

	a := test.NewApp()
	defer a.Quit()
	w := test.NewWindow(nil)
	defer w.Close()

	scanRes := &importer.ScanResult{
		VendorHint: "SamacSys",
		Candidates: []importer.CandidatePart{
			{Selected: true, PartName: "PMEG6010EXEX", Value: "PMEG6010EXEX"},
			{Selected: true, PartName: "PMEG6010EXEX", Value: "PMEG6010EXEX"},
			{Selected: true, PartName: "DR125-330-R", Value: "DR125-330-R"},
			{Selected: true, PartName: "DR125-330-R", Value: "DR125-330-R"},
		},
	}

	ShowDropStagingDialog(w, mgr, scanRes, nil)

	// Verify importing candidates directly with manager never fails unique constraint
	p1, err := mgr.ImportPart(storage.ImportPartRequest{LibraryID: lib.ID, PartName: "PMEG6010EXEX"})
	if err != nil {
		t.Fatalf("p1 failed: %v", err)
	}
	p2, err := mgr.ImportPart(storage.ImportPartRequest{LibraryID: lib.ID, PartName: "PMEG6010EXEX"})
	if err != nil {
		t.Fatalf("p2 failed: %v", err)
	}
	if p1.Name == p2.Name {
		t.Fatalf("expected distinct names for duplicate import, got %q and %q", p1.Name, p2.Name)
	}
}
