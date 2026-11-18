package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/importer"
	"github.com/jeffk/kicad-lib/internal/storage"
)

// ShowDropStagingDialog presents discovered symbols, footprints, 3D models, and metadata
// from a dropped zip/folder, Git repo, or LCSC lookup, allowing the user to pick an existing
// KiCad library or create a new library before importing and syncing.
func ShowDropStagingDialog(parent fyne.Window, mgr *storage.Manager, res *importer.ScanResult, onImported func()) {
	if res == nil || len(res.Candidates) == 0 {
		if res != nil {
			res.Cleanup()
		}
		msg := "No KiCad symbols (.kicad_sym / .lib) or footprints (.kicad_mod) were found."
		if res != nil && len(res.Warnings) > 0 {
			msg += "\n\nWarnings:\n" + strings.Join(res.Warnings, "\n")
		}
		dialog.ShowInformation("Nothing to Import", msg, parent)
		return
	}

	libs, _ := mgr.DB().ListLibraries()
	libNames := make([]string, 0, len(libs))
	for _, l := range libs {
		libNames = append(libNames, l.Name)
	}

	// Target library selection vs inline creation
	modeExisting := "Add to Existing Library"
	modeNew := "Create New Library"
	existingSelect := widget.NewSelect(libNames, nil)
	if len(libNames) > 0 {
		existingSelect.SetSelectedIndex(0)
	}
	newLibNameEntry := widget.NewEntry()
	newLibNameEntry.SetPlaceHolder("New library name (e.g. Custom_ICs)")
	newLibDescEntry := widget.NewEntry()
	newLibDescEntry.SetPlaceHolder("Optional library description")

	newLibBox := container.NewVBox(newLibNameEntry, newLibDescEntry)
	targetModeRadio := widget.NewRadioGroup([]string{modeExisting, modeNew}, func(sel string) {
		if sel == modeNew {
			existingSelect.Hide()
			newLibBox.Show()
		} else {
			existingSelect.Show()
			newLibBox.Hide()
		}
	})
	if len(libNames) > 0 {
		targetModeRadio.SetSelected(modeExisting)
		newLibBox.Hide()
	} else {
		targetModeRadio.SetSelected(modeNew)
		existingSelect.Hide()
	}

	// Candidate selection & active candidate editor
	activeIdx := 0

	nameEntry := widget.NewEntry()
	valEntry := widget.NewEntry()
	mpnEntry := widget.NewEntry()
	mfgEntry := widget.NewEntry()
	lcscEntry := widget.NewEntry()
	dsEntry := widget.NewEntry()
	descEntry := widget.NewEntry()

	symSummaryLbl := widget.NewLabel("")
	fpOptions := make([]string, 0, len(res.AllFootprints)+1)
	fpOptions = append(fpOptions, "(None)")
	for _, fp := range res.AllFootprints {
		fpOptions = append(fpOptions, fp.Name)
	}
	fpSelect := widget.NewSelect(fpOptions, nil)
	modelsLbl := widget.NewLabel("")
	modelsLbl.Wrapping = fyne.TextWrapWord

	metaPreviewLbl := widget.NewLabel("")
	metaPreviewLbl.Wrapping = fyne.TextWrapWord
	metaKeyEntry := widget.NewEntry()
	metaKeyEntry.SetPlaceHolder("Custom Metadata Key")
	metaValEntry := widget.NewEntry()
	metaValEntry.SetPlaceHolder("Value")

	var suppressSync bool
	refreshMetaPreview := func(c *importer.CandidatePart) {
		if len(c.Metadata) == 0 {
			metaPreviewLbl.SetText("(no custom metadata)")
			return
		}
		keys := make([]string, 0, len(c.Metadata))
		for k := range c.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var lines []string
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("%s = %s", k, c.Metadata[k]))
		}
		metaPreviewLbl.SetText(strings.Join(lines, "\n"))
	}

	loadCandidateIntoForm := func(idx int) {
		if idx < 0 || idx >= len(res.Candidates) {
			return
		}
		suppressSync = true
		defer func() { suppressSync = false }()

		activeIdx = idx
		c := &res.Candidates[idx]
		nameEntry.SetText(c.PartName)
		valEntry.SetText(c.Value)
		mpnEntry.SetText(c.MPN)
		mfgEntry.SetText(c.Manufacturer)
		lcscEntry.SetText(c.LCSCPart)
		dsEntry.SetText(c.Datasheet)
		descEntry.SetText(c.Description)

		if c.SymbolSExpr != "" {
			symSummaryLbl.SetText(fmt.Sprintf("%s (%d pins)", c.SymbolName, c.PinCount))
		} else {
			symSummaryLbl.SetText("(Footprint-only part)")
		}

		if c.FootprintName != "" {
			fpSelect.SetSelected(c.FootprintName)
		} else {
			fpSelect.SetSelected("(None)")
		}

		if len(c.Models3D) > 0 {
			var names []string
			for _, m := range c.Models3D {
				names = append(names, fmt.Sprintf("%s (%s)", m.FileName, strings.ToUpper(m.Format)))
			}
			modelsLbl.SetText(strings.Join(names, ", "))
		} else {
			modelsLbl.SetText("(none attached)")
		}
		refreshMetaPreview(c)
	}

	// Bind live edits back to res.Candidates[activeIdx]
	nameEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].PartName = s
		}
	}
	valEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].Value = s
		}
	}
	mpnEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].MPN = s
		}
	}
	mfgEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].Manufacturer = s
		}
	}
	lcscEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].LCSCPart = s
		}
	}
	dsEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].Datasheet = s
		}
	}
	descEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].Description = s
		}
	}
	fpSelect.OnChanged = func(sel string) {
		if suppressSync {
			return
		}
		c := &res.Candidates[activeIdx]
		if sel == "(None)" || sel == "" {
			c.FootprintName = ""
			c.FootprintSourcePath = ""
			c.FootprintSExpr = ""
			c.PadCount = 0
			return
		}
		for _, fp := range res.AllFootprints {
			if fp.Name == sel {
				c.FootprintName = fp.Name
				c.FootprintSourcePath = fp.SourcePath
				c.FootprintSExpr = fp.RawSExpr
				c.PadCount = fp.PadCount
				break
			}
		}
	}

	addMetaBtn := widget.NewButtonWithIcon("Add Metadata", theme.ContentAddIcon(), func() {
		k := strings.TrimSpace(metaKeyEntry.Text)
		v := strings.TrimSpace(metaValEntry.Text)
		if k == "" {
			return
		}
		c := &res.Candidates[activeIdx]
		if c.Metadata == nil {
			c.Metadata = make(map[string]string)
		}
		c.Metadata[k] = v
		metaKeyEntry.SetText("")
		metaValEntry.SetText("")
		refreshMetaPreview(c)
	})

	lookupBtn := widget.NewButtonWithIcon("Lookup Metadata", theme.SearchIcon(), func() {
		c := &res.Candidates[activeIdx]
		q := strings.TrimSpace(c.LCSCPart)
		if q == "" {
			q = strings.TrimSpace(c.MPN)
		}
		if q == "" {
			q = c.PartName
		}
		go func(idx int, query string) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			found, err := importer.LookupMetadata(ctx, mgr.Config(), query)
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, parent)
					return
				}
				target := &res.Candidates[idx]
				if target.Metadata == nil {
					target.Metadata = make(map[string]string)
				}
				for k, v := range found.Attributes {
					if target.Metadata[k] == "" {
						target.Metadata[k] = v
					}
				}
				if target.MPN == "" && found.MPN != "" {
					target.MPN = found.MPN
				}
				if target.Manufacturer == "" && found.Manufacturer != "" {
					target.Manufacturer = found.Manufacturer
				}
				if target.LCSCPart == "" && found.LCSCPart != "" {
					target.LCSCPart = found.LCSCPart
				}
				if target.Datasheet == "" && found.Datasheet != "" {
					target.Datasheet = found.Datasheet
				}
				if target.Description == "" && found.Description != "" {
					target.Description = found.Description
				}
				if activeIdx == idx {
					loadCandidateIntoForm(idx)
				}
			})
		}(activeIdx, q)
	})

	var candList *widget.List
	candList = widget.NewList(
		func() int { return len(res.Candidates) },
		func() fyne.CanvasObject {
			chk := widget.NewCheck("", nil)
			lbl := widget.NewLabel("Part Name")
			lbl.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, chk, nil, lbl)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			c := &res.Candidates[id]
			border := obj.(*fyne.Container)
			// Border objects slice order: content first, then left/right/top/bottom
			var chk *widget.Check
			var lbl *widget.Label
			for _, child := range border.Objects {
				if w, ok := child.(*widget.Check); ok {
					chk = w
				} else if w, ok := child.(*widget.Label); ok {
					lbl = w
				}
			}
			if chk != nil {
				chk.OnChanged = nil
				chk.SetChecked(c.Selected)
				chk.OnChanged = func(b bool) {
					res.Candidates[id].Selected = b
				}
			}
			if lbl != nil {
				fpTag := ""
				if c.FootprintName != "" {
					fpTag = " [" + c.FootprintName + "]"
				}
				m3Tag := ""
				if len(c.Models3D) > 0 {
					m3Tag = " +3D"
				}
				lbl.SetText(c.PartName + fpTag + m3Tag)
			}
		},
	)
	candList.OnSelected = func(id widget.ListItemID) {
		loadCandidateIntoForm(id)
	}

	selectAllBtn := widget.NewButton("Select All", func() {
		for i := range res.Candidates {
			res.Candidates[i].Selected = true
		}
		candList.Refresh()
	})
	selectNoneBtn := widget.NewButton("Select None", func() {
		for i := range res.Candidates {
			res.Candidates[i].Selected = false
		}
		candList.Refresh()
	})

	loadCandidateIntoForm(0)
	candList.Select(0)

	leftPane := container.NewBorder(
		widget.NewLabelWithStyle(fmt.Sprintf("Detected Parts (%d) — %s", len(res.Candidates), res.VendorHint), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(selectAllBtn, selectNoneBtn),
		nil, nil,
		candList,
	)

	rightForm := container.NewVScroll(container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Part Name", nameEntry),
			widget.NewFormItem("Value", valEntry),
			widget.NewFormItem("MPN", mpnEntry),
			widget.NewFormItem("Manufacturer", mfgEntry),
			widget.NewFormItem("LCSC ID", lcscEntry),
			widget.NewFormItem("Datasheet", dsEntry),
			widget.NewFormItem("Description", descEntry),
			widget.NewFormItem("Symbol", symSummaryLbl),
			widget.NewFormItem("Footprint", fpSelect),
			widget.NewFormItem("3D Models", modelsLbl),
		),
		widget.NewSeparator(),
		container.NewHBox(widget.NewLabelWithStyle("Metadata", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), layout.NewSpacer(), lookupBtn),
		metaPreviewLbl,
		container.NewGridWithColumns(2, metaKeyEntry, metaValEntry),
		addMetaBtn,
	))

	split := container.NewHSplit(leftPane, rightForm)
	split.Offset = 0.35

	targetCard := widget.NewCard("Target KiCad Library", "", container.NewVBox(
		targetModeRadio,
		existingSelect,
		newLibBox,
	))

	var dlg dialog.Dialog
	importBtn := widget.NewButtonWithIcon("Import & Sync to KiCad", theme.ConfirmIcon(), func() {
		var selected []importer.CandidatePart
		for _, c := range res.Candidates {
			if c.Selected {
				selected = append(selected, c)
			}
		}
		if len(selected) == 0 {
			dialog.ShowInformation("No Parts Selected", "Check at least one part to import.", parent)
			return
		}

		var targetLib *db.Library
		var err error
		if targetModeRadio.Selected == modeNew {
			libName := strings.TrimSpace(newLibNameEntry.Text)
			if libName == "" {
				dialog.ShowInformation("Library Name Required", "Enter a name for the new KiCad library.", parent)
				return
			}
			targetLib, err = mgr.DB().GetLibraryByName(libName)
			if err != nil {
				targetLib, err = mgr.DB().CreateLibrary(libName, strings.TrimSpace(newLibDescEntry.Text))
				if err != nil {
					dialog.ShowError(err, parent)
					return
				}
			}
		} else {
			libName := existingSelect.Selected
			if libName == "" {
				dialog.ShowInformation("Library Required", "Select an existing library or choose Create New Library.", parent)
				return
			}
			targetLib, err = mgr.DB().GetLibraryByName(libName)
			if err != nil {
				dialog.ShowError(err, parent)
				return
			}
		}

		var errs []string
		importedCount := 0
		for _, c := range selected {
			var models []storage.ImportModel3D
			for _, m := range c.Models3D {
				models = append(models, storage.ImportModel3D{
					SourcePath: m.SourcePath,
					FileName:   m.FileName,
					Format:     m.Format,
				})
			}
			_, err := mgr.ImportPart(storage.ImportPartRequest{
				LibraryID:           targetLib.ID,
				PartName:            c.PartName,
				Value:               c.Value,
				Description:         c.Description,
				Keywords:            c.Keywords,
				Datasheet:           c.Datasheet,
				MPN:                 c.MPN,
				Manufacturer:        c.Manufacturer,
				LCSCPart:            c.LCSCPart,
				SourceType:          c.SourceType,
				SourceRef:           c.SourceRef,
				SymbolName:          c.SymbolName,
				SymbolSourcePath:    c.SymbolSourcePath,
				SymbolSExpr:         c.SymbolSExpr,
				FootprintName:       c.FootprintName,
				FootprintSourcePath: c.FootprintSourcePath,
				FootprintSExpr:      c.FootprintSExpr,
				Models3D:            models,
				Metadata:            c.Metadata,
			})
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", c.PartName, err))
			} else {
				importedCount++
			}
		}

		dlg.Hide()
		if len(errs) > 0 {
			dialog.ShowInformation("Import Completed with Warnings",
				fmt.Sprintf("Imported %d part(s) into %q.\n\nErrors:\n%s", importedCount, targetLib.Name, strings.Join(errs, "\n")),
				parent)
		} else {
			dialog.ShowInformation("Import Complete",
				fmt.Sprintf("Imported %d part(s) into %q and synced KiCad library files.", importedCount, targetLib.Name),
				parent)
		}
		if onImported != nil {
			onImported()
		}
	})
	importBtn.Importance = widget.HighImportance

	cancelBtn := widget.NewButton("Cancel", func() {
		dlg.Hide()
	})

	body := container.NewBorder(
		targetCard,
		container.NewHBox(layout.NewSpacer(), cancelBtn, importBtn),
		nil, nil,
		split,
	)

	dlg = dialog.NewCustomWithoutButtons("Stage & Import Parts", body, parent)
	dlg.SetOnClosed(func() {
		res.Cleanup()
	})
	dlg.Resize(fyne.NewSize(900, 650))
	dlg.Show()
}
