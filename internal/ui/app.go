package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/importer"
	"github.com/jeffk/kicad-lib/internal/kicad"
	"github.com/jeffk/kicad-lib/internal/storage"
)

// App encapsulates the Fyne desktop application and UI state.
type App struct {
	fyneApp fyne.App
	win     fyne.Window
	mgr     *storage.Manager

	allLibraries       []db.Library
	visibleLibraries   []db.Library
	libraries          []db.Library
	selectedLibID      int64 // 0 = All Parts
	parts              []db.Part
	wasIPCConnected    bool
	wasInitialSyncDone bool

	libList        *widget.List
	libSearchEntry *widget.Entry
	toggleKiCadBtn *widget.Button
	partsList      *widget.List
	searchEntry  *widget.Entry
	metaKeyEntry *widget.Entry
	metaValEntry *widget.Entry
	only3DCheck  *widget.Check
	statusLbl    *widget.Label
	ipcStatusLbl *widget.Label
	inspector    *PartInspector
}

// NewApp constructs the main Fyne window and wires drag-and-drop, search, inspector, and KiCad sync.
func NewApp(mgr *storage.Manager) *App {
	fa := app.NewWithID("com.github.jeffk.kicad-lib")
	w := fa.NewWindow("KiCad Part Library Manager")
	w.Resize(fyne.NewSize(1320, 820))

	a := &App{
		fyneApp: fa,
		win:     w,
		mgr:     mgr,
	}
	a.buildUI()
	a.refreshData()
	a.checkIPCAsync()
	return a
}

// Run starts the Fyne event loop and periodically refreshes KiCad IPC status.
func (a *App) Run() {
	go func() {
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			a.checkIPCAsync()
		}
	}()
	a.win.ShowAndRun()
}

func (a *App) buildUI() {
	// Window-wide drag-and-drop handler for zip archives, folders, and KiCad files
	a.win.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) {
		var paths []string
		for _, u := range uris {
			if p := u.Path(); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) > 0 {
			a.handleDroppedPaths(paths)
		}
	})

	// Toolbar
	importZipBtn := widget.NewButtonWithIcon("Import Zip/File", theme.FileIcon(), a.openZipPicker)
	importFolderBtn := widget.NewButtonWithIcon("Import Folder", theme.FolderOpenIcon(), a.openFolderPicker)
	importGitBtn := widget.NewButtonWithIcon("Git Repo", theme.DownloadIcon(), func() {
		ShowGitImportDialog(a.win, a.mgr, a.refreshData)
	})
	importLCSCBtn := widget.NewButtonWithIcon("LCSC", theme.SearchIcon(), func() {
		ShowLCSCImportDialog(a.win, a.mgr, a.refreshData)
	})
	newLibBtn := widget.NewButtonWithIcon("New Library", theme.ContentAddIcon(), a.showCreateLibraryDialog)
	manageLibsBtn := widget.NewButtonWithIcon("Manage Libraries", theme.VisibilityIcon(), func() {
		ShowManageLibrariesWindow(a.win, a.mgr, a.refreshData)
	})
	regenBtn := widget.NewButtonWithIcon("Regenerate All", theme.ViewRefreshIcon(), a.handleRegenerateAll)
	exportBtn := widget.NewButtonWithIcon("Export .zip", theme.DocumentSaveIcon(), a.handleExportSelectedLibrary)
	settingsBtn := widget.NewButtonWithIcon("Settings", theme.SettingsIcon(), func() {
		ShowSettingsDialog(a.win, a.mgr, func() {
			a.checkIPCAsync()
			a.refreshData()
		})
	})

	a.ipcStatusLbl = widget.NewLabel("KiCad IPC: Checking...")
	ipcSyncBtn := widget.NewButtonWithIcon("Sync IPC", theme.MediaReplayIcon(), func() {
		a.ipcStatusLbl.SetText("KiCad IPC: Checking...")
		go func() {
			st := a.mgr.IPC().RefreshLibrariesAndNotify()
			fyne.Do(func() {
				a.wasIPCConnected = st.Connected
				a.applyIPCStatus(st)
				a.syncFromKiCadAsync(true)
			})
		}()
	})

	toolbar := container.NewVBox(
		container.NewHBox(
			importZipBtn,
			importFolderBtn,
			importGitBtn,
			importLCSCBtn,
			widget.NewSeparator(),
			newLibBtn,
			manageLibsBtn,
			regenBtn,
			exportBtn,
			layout.NewSpacer(),
			a.ipcStatusLbl,
			ipcSyncBtn,
			settingsBtn,
		),
		widget.NewSeparator(),
	)

	// Left Sidebar: Libraries
	a.libSearchEntry = widget.NewEntry()
	a.libSearchEntry.SetPlaceHolder("Search shown libraries...")
	a.libSearchEntry.OnChanged = func(_ string) {
		a.applyLibraryFilter()
	}

	a.libList = widget.NewList(
		func() int { return len(a.libraries) + 1 },
		func() fyne.CanvasObject {
			lbl := widget.NewLabel("Library Name (0)")
			lbl.Truncation = fyne.TextTruncateEllipsis
			return lbl
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			lbl := obj.(*widget.Label)
			if id == 0 {
				total := 0
				for _, l := range a.visibleLibraries {
					total += l.PartCount
				}
				lbl.TextStyle = fyne.TextStyle{Bold: true}
				lbl.SetText(fmt.Sprintf("All Parts (%d)", total))
				return
			}
			lib := a.libraries[id-1]
			lbl.TextStyle = fyne.TextStyle{}
			suffix := ""
			if !lib.KiCadEnabled {
				suffix = " [KiCad Off]"
			}
			lbl.SetText(fmt.Sprintf("%s (%d)%s", lib.Name, lib.PartCount, suffix))
		},
	)
	a.libList.OnSelected = func(id widget.ListItemID) {
		if id == 0 {
			a.selectedLibID = 0
		} else if id-1 < len(a.libraries) {
			a.selectedLibID = a.libraries[id-1].ID
		}
		a.updateSelectedLibraryControls()
		a.refreshParts()
	}

	manageSideBtn := widget.NewButtonWithIcon("Manage...", theme.VisibilityIcon(), func() {
		ShowManageLibrariesWindow(a.win, a.mgr, a.refreshData)
	})
	hideLibBtn := widget.NewButtonWithIcon("Hide Lib", theme.VisibilityOffIcon(), a.handleHideSelectedLibrary)
	a.toggleKiCadBtn = widget.NewButtonWithIcon("KiCad On/Off", theme.ConfirmIcon(), a.handleToggleSelectedLibraryKiCad)
	delLibBtn := widget.NewButtonWithIcon("Delete Lib", theme.DeleteIcon(), a.handleDeleteSelectedLibrary)
	regenLibBtn := widget.NewButtonWithIcon("Regen Lib", theme.ViewRefreshIcon(), a.handleRegenerateSelectedLibrary)
	sidebar := container.NewBorder(
		container.NewVBox(
			container.NewHBox(
				widget.NewLabelWithStyle("Libraries", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				layout.NewSpacer(),
				manageSideBtn,
			),
			a.libSearchEntry,
		),
		container.NewGridWithColumns(2, hideLibBtn, a.toggleKiCadBtn, regenLibBtn, delLibBtn),
		nil, nil,
		a.libList,
	)

	// Center Panel: Drop Banner + Search + Parts List
	dropBg := canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground))
	dropBg.CornerRadius = 6
	dropText := widget.NewLabelWithStyle(
		"Drop a .zip archive, folder, or KiCad project/schematic/PCB (.kicad_pro, .kicad_sch, .kicad_pcb) anywhere on this window to extract parts, footprints & 3D models",
		fyne.TextAlignCenter,
		fyne.TextStyle{Italic: true},
	)
	dropText.Wrapping = fyne.TextWrapWord
	dropBanner := container.NewStack(dropBg, container.NewPadded(dropText))

	a.searchEntry = widget.NewEntry()
	a.searchEntry.SetPlaceHolder("Search parts by name, MPN, LCSC, description, or metadata...")
	a.searchEntry.OnChanged = func(_ string) { a.refreshParts() }

	a.metaKeyEntry = widget.NewEntry()
	a.metaKeyEntry.SetPlaceHolder("Meta Key (e.g. Package)")
	a.metaKeyEntry.OnChanged = func(_ string) { a.refreshParts() }

	a.metaValEntry = widget.NewEntry()
	a.metaValEntry.SetPlaceHolder("Meta Value (e.g. QFN)")
	a.metaValEntry.OnChanged = func(_ string) { a.refreshParts() }

	a.only3DCheck = widget.NewCheck("Has 3D", func(_ bool) { a.refreshParts() })
	clearSearchBtn := widget.NewButton("Clear", func() {
		a.searchEntry.SetText("")
		a.metaKeyEntry.SetText("")
		a.metaValEntry.SetText("")
		a.only3DCheck.SetChecked(false)
		a.refreshParts()
	})

	searchBar := container.NewVBox(
		dropBanner,
		a.searchEntry,
		container.NewBorder(nil, nil, nil, container.NewHBox(a.only3DCheck, clearSearchBtn),
			container.NewGridWithColumns(2, a.metaKeyEntry, a.metaValEntry)),
		widget.NewSeparator(),
		container.NewGridWithColumns(5,
			widget.NewLabelWithStyle("Part", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Library", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Footprint", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("3D / MPN", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Description", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		),
	)

	a.partsList = widget.NewList(
		func() int { return len(a.parts) },
		func() fyne.CanvasObject {
			cells := make([]fyne.CanvasObject, 5)
			for i := 0; i < 5; i++ {
				l := widget.NewLabel("")
				l.Truncation = fyne.TextTruncateEllipsis
				cells[i] = l
			}
			return container.NewGridWithColumns(5, cells...)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			p := a.parts[id]
			grid := obj.(*fyne.Container)
			fpName := "-"
			if p.Footprint != nil {
				fpName = p.Footprint.Name
			}
			tag3D := "No 3D"
			if len(p.Models3D) > 0 {
				tag3D = strings.ToUpper(p.Models3D[0].Format)
			}
			if p.LCSCPart != "" {
				tag3D += " | " + p.LCSCPart
			} else if p.MPN != "" {
				tag3D += " | " + p.MPN
			}

			vals := []string{p.Name, p.LibraryName, fpName, tag3D, p.Description}
			for i := 0; i < 5; i++ {
				grid.Objects[i].(*widget.Label).SetText(vals[i])
			}
		},
	)
	a.partsList.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(a.parts) {
			full, err := a.mgr.DB().GetPart(a.parts[id].ID)
			if err == nil {
				a.inspector.SetPart(full)
			} else {
				a.inspector.SetPart(&a.parts[id])
			}
		}
	}

	centerPane := container.NewBorder(searchBar, nil, nil, nil, a.partsList)

	// Right Panel: Inspector
	a.inspector = NewPartInspector(a.win, a.mgr, a.refreshData)

	rightSplit := container.NewHSplit(centerPane, a.inspector.CanvasObject())
	rightSplit.Offset = 0.62

	mainSplit := container.NewHSplit(sidebar, rightSplit)
	mainSplit.Offset = 0.20

	a.statusLbl = widget.NewLabel("Ready")
	statusBar := container.NewVBox(
		widget.NewSeparator(),
		container.NewHBox(
			a.statusLbl,
			layout.NewSpacer(),
			widget.NewLabel("Storage: "+a.mgr.Config().StorageRoot),
		),
	)

	a.win.SetContent(container.NewBorder(toolbar, statusBar, nil, nil, mainSplit))
}

func (a *App) refreshData() {
	libs, err := a.mgr.DB().ListLibraries()
	if err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	a.allLibraries = libs
	visible := make([]db.Library, 0, len(libs))
	selectedStillVisible := a.selectedLibID == 0
	for _, l := range libs {
		if !l.Hidden {
			visible = append(visible, l)
			if l.ID == a.selectedLibID {
				selectedStillVisible = true
			}
		}
	}
	a.visibleLibraries = visible
	if !selectedStillVisible {
		a.selectedLibID = 0
		a.libList.Select(0)
	}
	a.applyLibraryFilter()
	a.inspector.SetLibraries(libs)
	a.refreshParts()
}

func (a *App) applyLibraryFilter() {
	query := ""
	if a.libSearchEntry != nil {
		query = a.libSearchEntry.Text
	}
	a.libraries = filterLibraries(a.visibleLibraries, query, false, false)
	if a.libList != nil {
		a.libList.Refresh()
	}
	a.updateSelectedLibraryControls()
}

func (a *App) updateSelectedLibraryControls() {
	if a.toggleKiCadBtn == nil {
		return
	}
	if a.selectedLibID <= 0 {
		a.toggleKiCadBtn.SetText("KiCad On/Off")
		return
	}
	for _, l := range a.visibleLibraries {
		if l.ID == a.selectedLibID {
			if l.KiCadEnabled {
				a.toggleKiCadBtn.SetText("Disable KiCad")
			} else {
				a.toggleKiCadBtn.SetText("Enable KiCad")
			}
			return
		}
	}
	a.toggleKiCadBtn.SetText("KiCad On/Off")
}

func (a *App) refreshParts() {
	filter := db.SearchFilter{
		Query:         strings.TrimSpace(a.searchEntry.Text),
		LibraryID:     a.selectedLibID,
		ExcludeHidden: true,
	}
	mk := strings.TrimSpace(a.metaKeyEntry.Text)
	mv := strings.TrimSpace(a.metaValEntry.Text)
	if mk != "" || mv != "" {
		filter.MetadataFilters = map[string]string{mk: mv}
	}
	if a.only3DCheck.Checked {
		b := true
		filter.Has3DModel = &b
	}

	parts, err := a.mgr.DB().SearchParts(filter)
	if err != nil {
		a.statusLbl.SetText("Search error: " + err.Error())
		return
	}
	a.parts = parts
	a.partsList.Refresh()
	totalParts := 0
	for _, l := range a.visibleLibraries {
		totalParts += l.PartCount
	}
	switch {
	case len(a.visibleLibraries) == 0:
		a.statusLbl.SetText(fmt.Sprintf("0 part(s) across 0 shown library(ies) (%d hidden — click 'Manage Libraries' to un-hide)", len(a.allLibraries)))
	case a.selectedLibID == 0 && strings.TrimSpace(a.searchEntry.Text) == "" && totalParts > len(parts):
		a.statusLbl.SetText(fmt.Sprintf("Showing %d of %d part(s) across %d shown library(ies) (search or select a library)", len(parts), totalParts, len(a.visibleLibraries)))
	default:
		a.statusLbl.SetText(fmt.Sprintf("%d part(s) across %d shown library(ies)", len(parts), len(a.visibleLibraries)))
	}
}

func (a *App) handleDroppedPaths(paths []string) {
	a.statusLbl.SetText("Scanning dropped archive/folder...")
	cliPath := a.mgr.Config().KiCadCLIPath
	go func() {
		res, err := importer.ScanDroppedPaths(paths, cliPath)
		fyne.Do(func() {
			if err != nil {
				a.statusLbl.SetText("Scan failed")
				dialog.ShowError(err, a.win)
				return
			}
			a.statusLbl.SetText("Ready")
			ShowDropStagingDialog(a.win, a.mgr, res, a.refreshData)
		})
	}()
}

func (a *App) openZipPicker() {
	dialog.ShowFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, a.win)
			return
		}
		if rc == nil {
			return
		}
		p := rc.URI().Path()
		_ = rc.Close()
		a.handleDroppedPaths([]string{p})
	}, a.win)
}

func (a *App) openFolderPicker() {
	dialog.ShowFolderOpen(func(lu fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, a.win)
			return
		}
		if lu == nil {
			return
		}
		a.handleDroppedPaths([]string{lu.Path()})
	}, a.win)
}

func (a *App) showCreateLibraryDialog() {
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("e.g. Sensors_Custom")
	descEntry := widget.NewEntry()
	descEntry.SetPlaceHolder("Optional description")

	dialog.ShowForm("Create New KiCad Library", "Create", "Cancel", []*widget.FormItem{
		widget.NewFormItem("Library Name", nameEntry),
		widget.NewFormItem("Description", descEntry),
	}, func(ok bool) {
		if !ok {
			return
		}
		lib, err := a.mgr.DB().CreateLibrary(nameEntry.Text, descEntry.Text)
		if err != nil {
			dialog.ShowError(err, a.win)
			return
		}
		_ = a.mgr.RegenerateLibrary(lib.ID)
		a.refreshData()
	}, a.win)
}

func (a *App) handleRegenerateAll() {
	a.statusLbl.SetText("Regenerating all KiCad libraries...")
	go func() {
		err := a.mgr.RegenerateAllLibraries()
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, a.win)
				a.statusLbl.SetText("Regenerate failed")
				return
			}
			a.statusLbl.SetText("All KiCad libraries regenerated and synced.")
			dialog.ShowInformation("Libraries Synced", "All KiCad symbol, footprint, and 3D model libraries have been regenerated.", a.win)
		})
	}()
}

func (a *App) handleRegenerateSelectedLibrary() {
	if a.selectedLibID <= 0 {
		a.handleRegenerateAll()
		return
	}
	if err := a.mgr.RegenerateLibrary(a.selectedLibID); err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	a.statusLbl.SetText("Library regenerated on disk.")
}

func (a *App) handleHideSelectedLibrary() {
	if a.selectedLibID <= 0 {
		dialog.ShowInformation("Hide Library", "Select a specific library in the sidebar first.", a.win)
		return
	}
	if err := a.mgr.DB().SetLibraryHidden(a.selectedLibID, true); err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	a.selectedLibID = 0
	a.libList.Select(0)
	a.inspector.SetPart(nil)
	a.refreshData()
}

func (a *App) handleToggleSelectedLibraryKiCad() {
	if a.selectedLibID <= 0 {
		dialog.ShowInformation("KiCad Library Status", "Select a specific library in the sidebar first.", a.win)
		return
	}
	lib, err := a.mgr.DB().GetLibrary(a.selectedLibID)
	if err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	nextEnabled := !lib.KiCadEnabled
	if err := a.mgr.SetLibraryKiCadEnabled(lib.ID, nextEnabled); err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	a.refreshData()
	if nextEnabled {
		a.statusLbl.SetText(fmt.Sprintf("Enabled library %q in KiCad.", lib.Name))
	} else {
		a.statusLbl.SetText(fmt.Sprintf("Disabled library %q in KiCad.", lib.Name))
	}
}

func (a *App) handleDeleteSelectedLibrary() {
	if a.selectedLibID <= 0 {
		dialog.ShowInformation("Delete Library", "Select a specific library in the sidebar first.", a.win)
		return
	}
	lib, err := a.mgr.DB().GetLibrary(a.selectedLibID)
	if err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	dialog.ShowConfirm("Delete Library",
		fmt.Sprintf("Delete library %q and all %d part(s) inside it?", lib.Name, lib.PartCount),
		func(ok bool) {
			if !ok {
				return
			}
			if err := a.mgr.DeleteLibrary(lib.ID); err != nil {
				dialog.ShowError(err, a.win)
				return
			}
			a.selectedLibID = 0
			a.libList.Select(0)
			a.inspector.SetPart(nil)
			a.refreshData()
		}, a.win)
}

func (a *App) handleExportSelectedLibrary() {
	if a.selectedLibID <= 0 {
		dialog.ShowInformation("Export Library", "Select a specific library from the left sidebar to export.", a.win)
		return
	}
	lib, err := a.mgr.DB().GetLibrary(a.selectedLibID)
	if err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	dlg := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, a.win)
			return
		}
		if wc == nil {
			return
		}
		outPath := wc.URI().Path()
		_ = wc.Close()
		if !strings.HasSuffix(strings.ToLower(outPath), ".zip") {
			outPath += ".zip"
		}
		if err := a.mgr.ExportLibraryArchive(lib.ID, outPath); err != nil {
			dialog.ShowError(err, a.win)
			return
		}
		dialog.ShowInformation("Export Complete", fmt.Sprintf("Exported %q to:\n%s", lib.Name, outPath), a.win)
	}, a.win)
	dlg.SetFileName(lib.Name + ".zip")
	dlg.Show()
}

func (a *App) checkIPCAsync() {
	ipc := a.mgr.IPC()
	if ipc == nil {
		if !a.wasInitialSyncDone {
			a.wasInitialSyncDone = true
			a.syncFromKiCadAsync(false)
		}
		return
	}
	go func() {
		st := ipc.CheckStatus()
		fyne.Do(func() { a.applyIPCStatus(st) })
	}()
}

func (a *App) applyIPCStatus(st kicad.IPCStatus) {
	if st.Connected {
		justConnected := !a.wasIPCConnected
		a.wasIPCConnected = true

		docCount := len(st.OpenDocuments)
		if docCount > 0 {
			a.ipcStatusLbl.SetText(fmt.Sprintf("KiCad IPC: %s (%d open)", st.KiCadVersion, docCount))
		} else {
			a.ipcStatusLbl.SetText(fmt.Sprintf("KiCad IPC: %s", st.KiCadVersion))
		}
		if justConnected || !a.wasInitialSyncDone {
			a.wasInitialSyncDone = true
			a.syncFromKiCadAsync(false)
		}
		return
	}
	a.wasIPCConnected = false
	a.ipcStatusLbl.SetText("KiCad IPC: Offline (File Sync Active)")
	if !a.wasInitialSyncDone {
		a.wasInitialSyncDone = true
		a.syncFromKiCadAsync(false)
	}
}

func (a *App) syncFromKiCadAsync(showSummary bool) {
	go func() {
		lastUpdate := time.Time{}
		libsSynced, partsSynced, err := a.mgr.SyncFromKiCad(func(p storage.SyncProgress) {
			if time.Since(lastUpdate) < 120*time.Millisecond && p.Current < p.Total {
				return
			}
			lastUpdate = time.Now()
			fyne.Do(func() {
				a.statusLbl.SetText(fmt.Sprintf("Syncing KiCad libraries (%d/%d): %s...", p.Current, p.Total, p.LibraryName))
			})
		})
		fyne.Do(func() {
			if err != nil {
				a.statusLbl.SetText("KiCad sync error: " + err.Error())
				if showSummary {
					dialog.ShowError(err, a.win)
				}
				return
			}
			a.refreshData()
			if showSummary {
				if libsSynced > 0 {
					dialog.ShowInformation("KiCad Libraries Synced",
						fmt.Sprintf("Pulled and synced %d library(ies) (%d part(s)) from KiCad.", libsSynced, partsSynced),
						a.win)
				} else {
					dialog.ShowInformation("KiCad Libraries Up to Date",
						"All existing KiCad libraries are already up to date.",
						a.win)
				}
			}
		})
	}()
}
