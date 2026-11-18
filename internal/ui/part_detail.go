package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/jeffk/kicad-lib/internal/kicad"
	"github.com/jeffk/kicad-lib/internal/storage"
)

// PartInspector manages the right-hand inspector panel for the currently selected part.
type PartInspector struct {
	win       fyne.Window
	mgr       *storage.Manager
	onChanged func()

	root       *fyne.Container
	emptyLabel *widget.Label
	contentBox *fyne.Container

	part       *db.Part
	libraries  []db.Library
	editMeta   map[string]string
	metaKeys   []string

	titleLbl     *widget.Label
	libBadge     *widget.Label
	moveSelect   *widget.Select
	moveBtn      *widget.Button
	deleteBtn    *widget.Button

	valEntry     *widget.Entry
	mpnEntry     *widget.Entry
	mfgEntry     *widget.Entry
	lcscEntry    *widget.Entry
	dsEntry      *widget.Entry
	descEntry    *widget.Entry
	kwEntry      *widget.Entry
	saveInfoBtn  *widget.Button

	symInfoLbl   *widget.Label
	fpInfoLbl    *widget.Label
	modelsLbl    *widget.Label
	symPreview   *InteractivePreviewWidget
	fpPreview    *InteractivePreviewWidget
	m3dPreview   *InteractivePreviewWidget
	modelSelect  *widget.Select
	attach3DBtn  *widget.Button

	metaListBox  *fyne.Container
	newKeyEntry  *widget.Entry
	newValEntry  *widget.Entry
	enrichBtn    *widget.Button
	saveMetaBtn  *widget.Button
}

// NewPartInspector builds the part inspector UI component.
func NewPartInspector(win fyne.Window, mgr *storage.Manager, onChanged func()) *PartInspector {
	pi := &PartInspector{
		win:       win,
		mgr:       mgr,
		onChanged: onChanged,
		editMeta:  make(map[string]string),
	}

	pi.emptyLabel = widget.NewLabel("Select a part to inspect symbol, footprint, 3D models, and metadata.")
	pi.emptyLabel.Wrapping = fyne.TextWrapWord

	pi.titleLbl = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	pi.libBadge = widget.NewLabel("")

	pi.moveSelect = widget.NewSelect(nil, nil)
	pi.moveSelect.PlaceHolder = "Move to library..."
	pi.moveBtn = widget.NewButtonWithIcon("Move", theme.MoveUpIcon(), pi.handleMovePart)
	pi.deleteBtn = widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), pi.handleDeletePart)
	pi.deleteBtn.Importance = widget.DangerImportance

	pi.valEntry = widget.NewEntry()
	pi.mpnEntry = widget.NewEntry()
	pi.mfgEntry = widget.NewEntry()
	pi.lcscEntry = widget.NewEntry()
	pi.dsEntry = widget.NewEntry()
	pi.descEntry = widget.NewMultiLineEntry()
	pi.descEntry.SetMinRowsVisible(2)
	pi.kwEntry = widget.NewEntry()
	pi.saveInfoBtn = widget.NewButtonWithIcon("Save Details & Sync", theme.DocumentSaveIcon(), pi.handleSaveDetails)
	pi.saveInfoBtn.Importance = widget.HighImportance

	detailsForm := widget.NewForm(
		widget.NewFormItem("Value", pi.valEntry),
		widget.NewFormItem("MPN", pi.mpnEntry),
		widget.NewFormItem("Manufacturer", pi.mfgEntry),
		widget.NewFormItem("LCSC ID", pi.lcscEntry),
		widget.NewFormItem("Datasheet", pi.dsEntry),
		widget.NewFormItem("Keywords", pi.kwEntry),
		widget.NewFormItem("Description", pi.descEntry),
	)
	detailsTab := container.NewVScroll(container.NewVBox(
		detailsForm,
		container.NewHBox(layout.NewSpacer(), pi.saveInfoBtn),
	))

	// Assets & Previews Tab
	pi.symInfoLbl = widget.NewLabel("No symbol")
	pi.symInfoLbl.Wrapping = fyne.TextWrapWord
	pi.fpInfoLbl = widget.NewLabel("No footprint")
	pi.fpInfoLbl.Wrapping = fyne.TextWrapWord
	pi.modelsLbl = widget.NewLabel("No 3D models")
	pi.modelsLbl.Wrapping = fyne.TextWrapWord

	pi.symPreview = NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(260, 200))
	pi.fpPreview = NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(260, 200))
	pi.m3dPreview = NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(260, 220))
	pi.modelSelect = widget.NewSelect(nil, func(sel string) {
		pi.loadSelected3DModel(sel)
	})
	pi.modelSelect.Hide()
	pi.attach3DBtn = widget.NewButtonWithIcon("Attach 3D Model (.step/.wrl)...", theme.FolderOpenIcon(), pi.handleAttach3D)

	symPanel := NewPreviewPanel(win, "Schematic Symbol", pi.symPreview, false)
	fpPanel := NewPreviewPanel(win, "PCB Footprint", pi.fpPreview, false)
	m3dPanel := NewPreviewPanel(win, "3D Model", pi.m3dPreview, true)

	assetsTab := container.NewVScroll(container.NewVBox(
		widget.NewCard("Schematic Symbol", "", container.NewVBox(pi.symInfoLbl, symPanel)),
		widget.NewCard("PCB Footprint", "", container.NewVBox(pi.fpInfoLbl, fpPanel)),
		widget.NewCard("3D Model", "", container.NewVBox(pi.modelsLbl, pi.modelSelect, m3dPanel, pi.attach3DBtn)),
	))

	// Metadata Editor Tab
	pi.metaListBox = container.NewVBox()
	pi.newKeyEntry = widget.NewEntry()
	pi.newKeyEntry.SetPlaceHolder("Key (e.g. Package)")
	pi.newValEntry = widget.NewEntry()
	pi.newValEntry.SetPlaceHolder("Value (e.g. QFN-48)")
	addMetaBtn := widget.NewButtonWithIcon("Add", theme.ContentAddIcon(), pi.handleAddMetadataField)

	pi.enrichBtn = widget.NewButtonWithIcon("Enrich (LCSC/Distributor)", theme.SearchIcon(), pi.handleEnrichMetadata)
	pi.saveMetaBtn = widget.NewButtonWithIcon("Save Metadata & Sync", theme.DocumentSaveIcon(), pi.handleSaveMetadata)
	pi.saveMetaBtn.Importance = widget.HighImportance

	metaTab := container.NewBorder(
		container.NewVBox(
			container.NewGridWithColumns(2, pi.newKeyEntry, pi.newValEntry),
			container.NewHBox(addMetaBtn, layout.NewSpacer(), pi.enrichBtn),
			widget.NewSeparator(),
		),
		container.NewHBox(layout.NewSpacer(), pi.saveMetaBtn),
		nil, nil,
		container.NewVScroll(pi.metaListBox),
	)

	tabs := container.NewAppTabs(
		container.NewTabItemWithIcon("Assets", theme.VisibilityIcon(), assetsTab),
		container.NewTabItemWithIcon("Details", theme.DocumentIcon(), detailsTab),
		container.NewTabItemWithIcon("Metadata", theme.ListIcon(), metaTab),
	)

	header := container.NewVBox(
		container.NewHBox(pi.titleLbl, layout.NewSpacer(), pi.deleteBtn),
		pi.libBadge,
		container.NewBorder(nil, nil, nil, pi.moveBtn, pi.moveSelect),
		widget.NewSeparator(),
	)

	pi.contentBox = container.NewBorder(header, nil, nil, nil, tabs)
	pi.contentBox.Hide()

	pi.root = container.NewStack(
		container.NewCenter(pi.emptyLabel),
		pi.contentBox,
	)
	return pi
}

// CanvasObject returns the root Fyne container.
func (pi *PartInspector) CanvasObject() fyne.CanvasObject {
	return pi.root
}

// SetLibraries updates the target library options in the Move dropdown.
func (pi *PartInspector) SetLibraries(libs []db.Library) {
	pi.libraries = libs
	names := make([]string, 0, len(libs))
	for _, l := range libs {
		names = append(names, l.Name)
	}
	pi.moveSelect.Options = names
	pi.moveSelect.Refresh()
}

// SetPart populates the inspector with the given part (or clears it if nil).
func (pi *PartInspector) SetPart(p *db.Part) {
	pi.part = p
	if p == nil {
		pi.contentBox.Hide()
		pi.emptyLabel.Show()
		return
	}

	pi.emptyLabel.Hide()
	pi.contentBox.Show()

	pi.titleLbl.SetText(p.Name)
	pi.libBadge.SetText(fmt.Sprintf("Library: %s   |   Source: %s", p.LibraryName, p.SourceType))
	pi.moveSelect.SetSelected(p.LibraryName)

	pi.valEntry.SetText(p.Value)
	pi.mpnEntry.SetText(p.MPN)
	pi.mfgEntry.SetText(p.Manufacturer)
	pi.lcscEntry.SetText(p.LCSCPart)
	pi.dsEntry.SetText(p.Datasheet)
	pi.descEntry.SetText(p.Description)
	pi.kwEntry.SetText(p.Keywords)

	if p.Symbol != nil {
		pi.symInfoLbl.SetText(fmt.Sprintf("Symbol: %s (%d pins)\nVault: %s", p.Symbol.Name, p.Symbol.PinCount, filepath.Base(p.Symbol.VaultPath)))
	} else {
		pi.symInfoLbl.SetText("No symbol attached")
	}

	if p.Footprint != nil {
		pi.fpInfoLbl.SetText(fmt.Sprintf("Footprint: %s:%s (%d pads)\nVault: %s", p.LibraryName, p.Footprint.Name, p.Footprint.PadCount, filepath.Base(p.Footprint.VaultPath)))
	} else {
		pi.fpInfoLbl.SetText("No footprint attached")
	}

	if len(p.Models3D) > 0 {
		var lines []string
		for _, m := range p.Models3D {
			lines = append(lines, fmt.Sprintf("• %s (%s) -> %s.3dshapes/%s", m.FileName, strings.ToUpper(m.Format), p.LibraryName, m.FileName))
		}
		pi.modelsLbl.SetText(strings.Join(lines, "\n"))
	} else {
		pi.modelsLbl.SetText("No 3D models attached")
	}

	// Copy metadata map for editing
	pi.editMeta = make(map[string]string, len(p.Metadata))
	for k, v := range p.Metadata {
		pi.editMeta[k] = v
	}
	pi.rebuildMetadataList()

	// Render interactive 2D Symbol preview (works immediately without SVG)
	if p.Symbol != nil && (p.Symbol.RawSExpr != "" || p.Symbol.VaultPath != "") {
		var d2 *kicad.Drawing2D
		var err error
		if p.Symbol.VaultPath != "" && strings.Contains(p.Symbol.RawSExpr, "(extends ") {
			d2, err = kicad.ParseSymbolDrawingFromFile(p.Symbol.VaultPath, p.Symbol.Name)
		}
		if d2 == nil && p.Symbol.RawSExpr != "" {
			d2, err = kicad.ParseSymbolDrawing(p.Symbol.RawSExpr)
		}
		if d2 == nil && p.Symbol.VaultPath != "" {
			d2, err = kicad.ParseSymbolDrawingFromFile(p.Symbol.VaultPath, p.Symbol.Name)
		}
		if err == nil && d2 != nil {
			pi.symPreview.SetDrawing2D(d2)
		} else {
			pi.symPreview.SetEmpty("Could not parse symbol geometry")
		}
	} else {
		pi.symPreview.SetEmpty("No symbol attached")
	}

	// Render interactive 2D Footprint preview (works even without an SVG)
	if p.Footprint != nil && (p.Footprint.RawSExpr != "" || p.Footprint.VaultPath != "") {
		rawFP := p.Footprint.RawSExpr
		if rawFP == "" && p.Footprint.VaultPath != "" {
			if b, err := os.ReadFile(p.Footprint.VaultPath); err == nil {
				rawFP = string(b)
			}
		}
		if rawFP != "" {
			if d2, err := kicad.ParseFootprintDrawing(rawFP); err == nil {
				pi.fpPreview.SetDrawing2D(d2)
			} else {
				pi.fpPreview.SetEmpty("Could not parse footprint geometry")
			}
		} else {
			pi.fpPreview.SetEmpty("Footprint file not found on disk")
		}
	} else {
		pi.fpPreview.SetEmpty("No footprint attached")
	}

	// Load 3D model preview asynchronously
	if len(p.Models3D) > 0 {
		names := make([]string, 0, len(p.Models3D))
		for _, m := range p.Models3D {
			names = append(names, m.FileName)
		}
		pi.modelSelect.Options = names
		if len(names) > 1 {
			pi.modelSelect.Show()
		} else {
			pi.modelSelect.Hide()
		}
		pi.modelSelect.Selected = names[0]
		pi.modelSelect.Refresh()
		pi.loadSelected3DModel(names[0])
	} else {
		pi.modelSelect.Options = nil
		pi.modelSelect.Hide()
		pi.m3dPreview.SetEmpty("No 3D models attached")
	}

	// Warm SVG cache in background via kicad-cli (also fixes .pretty directory handling)
	partSnapshot := *p
	go func() {
		_, _, _ = pi.mgr.RenderPartPreviews(&partSnapshot)
	}()
}

func (pi *PartInspector) loadSelected3DModel(fileName string) {
	if pi.part == nil || len(pi.part.Models3D) == 0 {
		pi.m3dPreview.SetEmpty("No 3D models attached")
		return
	}
	var target *db.Model3D
	for i := range pi.part.Models3D {
		if pi.part.Models3D[i].FileName == fileName {
			target = &pi.part.Models3D[i]
			break
		}
	}
	if target == nil {
		target = &pi.part.Models3D[0]
	}
	if target.VaultPath == "" {
		pi.m3dPreview.SetEmpty("3D model path not set")
		return
	}
	resolvedPath := kicad.Resolve3DModelPath(target.VaultPath)
	if _, err := os.Stat(resolvedPath); err != nil {
		pi.m3dPreview.SetEmpty("3D model file not found on disk:\n" + target.FileName)
		return
	}

	partID := pi.part.ID
	modelPath := resolvedPath
	modelName := filepath.Base(resolvedPath)
	pi.m3dPreview.SetEmpty("Loading 3D model " + modelName + "...")

	go func() {
		mesh, err := kicad.LoadMesh3D(modelPath)
		fyne.Do(func() {
			if pi.part == nil || pi.part.ID != partID {
				return
			}
			if err != nil {
				pi.m3dPreview.SetEmpty(fmt.Sprintf("Could not render %s:\n%v", modelName, err))
				return
			}
			pi.m3dPreview.SetMesh3D(mesh)
		})
	}()
}

func (pi *PartInspector) rebuildMetadataList() {
	pi.metaKeys = make([]string, 0, len(pi.editMeta))
	for k := range pi.editMeta {
		pi.metaKeys = append(pi.metaKeys, k)
	}
	sort.Strings(pi.metaKeys)

	var rows []fyne.CanvasObject
	for _, k := range pi.metaKeys {
		key := k
		keyLbl := widget.NewLabelWithStyle(key, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		valEntry := widget.NewEntry()
		valEntry.SetText(pi.editMeta[key])
		valEntry.OnChanged = func(s string) {
			pi.editMeta[key] = s
		}
		delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
			delete(pi.editMeta, key)
			pi.rebuildMetadataList()
		})
		row := container.NewBorder(nil, nil, keyLbl, delBtn, valEntry)
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, widget.NewLabel("No custom metadata fields yet. Add one above or click Enrich."))
	}
	pi.metaListBox.Objects = rows
	pi.metaListBox.Refresh()
}

func (pi *PartInspector) handleAddMetadataField() {
	k := strings.TrimSpace(pi.newKeyEntry.Text)
	v := strings.TrimSpace(pi.newValEntry.Text)
	if k == "" {
		dialog.ShowInformation("Metadata", "Please enter a metadata key name.", pi.win)
		return
	}
	pi.editMeta[k] = v
	pi.newKeyEntry.SetText("")
	pi.newValEntry.SetText("")
	pi.rebuildMetadataList()
}

func (pi *PartInspector) handleSaveMetadata() {
	if pi.part == nil {
		return
	}
	updated, err := pi.mgr.DB().GetPart(pi.part.ID)
	if err != nil {
		dialog.ShowError(err, pi.win)
		return
	}
	updated.Metadata = copyStrMap(pi.editMeta)
	if err := pi.mgr.UpdatePart(updated); err != nil {
		dialog.ShowError(err, pi.win)
		return
	}
	if refreshed, err := pi.mgr.DB().GetPart(pi.part.ID); err == nil {
		pi.SetPart(refreshed)
	}
	if pi.onChanged != nil {
		pi.onChanged()
	}
}

func (pi *PartInspector) handleSaveDetails() {
	if pi.part == nil {
		return
	}
	updated, err := pi.mgr.DB().GetPart(pi.part.ID)
	if err != nil {
		dialog.ShowError(err, pi.win)
		return
	}
	updated.Value = strings.TrimSpace(pi.valEntry.Text)
	updated.MPN = strings.TrimSpace(pi.mpnEntry.Text)
	updated.Manufacturer = strings.TrimSpace(pi.mfgEntry.Text)
	updated.LCSCPart = strings.TrimSpace(pi.lcscEntry.Text)
	updated.Datasheet = strings.TrimSpace(pi.dsEntry.Text)
	updated.Description = strings.TrimSpace(pi.descEntry.Text)
	updated.Keywords = strings.TrimSpace(pi.kwEntry.Text)
	updated.Metadata = copyStrMap(pi.editMeta)

	if err := pi.mgr.UpdatePart(updated); err != nil {
		dialog.ShowError(err, pi.win)
		return
	}
	if refreshed, err := pi.mgr.DB().GetPart(pi.part.ID); err == nil {
		pi.SetPart(refreshed)
	}
	if pi.onChanged != nil {
		pi.onChanged()
	}
}

func (pi *PartInspector) handleMovePart() {
	if pi.part == nil {
		return
	}
	targetName := pi.moveSelect.Selected
	var targetID int64
	for _, l := range pi.libraries {
		if l.Name == targetName {
			targetID = l.ID
			break
		}
	}
	if targetID <= 0 || targetID == pi.part.LibraryID {
		return
	}
	if err := pi.mgr.MovePart(pi.part.ID, targetID); err != nil {
		dialog.ShowError(err, pi.win)
		return
	}
	if refreshed, err := pi.mgr.DB().GetPart(pi.part.ID); err == nil {
		pi.SetPart(refreshed)
	}
	if pi.onChanged != nil {
		pi.onChanged()
	}
}

func (pi *PartInspector) handleDeletePart() {
	if pi.part == nil {
		return
	}
	target := pi.part
	dialog.ShowConfirm("Delete Part", fmt.Sprintf("Delete %q from %q and remove its vault assets?", target.Name, target.LibraryName), func(ok bool) {
		if !ok {
			return
		}
		if err := pi.mgr.DeletePart(target.ID); err != nil {
			dialog.ShowError(err, pi.win)
			return
		}
		pi.SetPart(nil)
		if pi.onChanged != nil {
			pi.onChanged()
		}
	}, pi.win)
}

func (pi *PartInspector) handleAttach3D() {
	if pi.part == nil {
		return
	}
	partID := pi.part.ID
	dialog.ShowFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, pi.win)
			return
		}
		if rc == nil {
			return
		}
		path := rc.URI().Path()
		_ = rc.Close()

		if err := pi.mgr.Attach3DModel(partID, path); err != nil {
			dialog.ShowError(err, pi.win)
			return
		}
		if refreshed, err := pi.mgr.DB().GetPart(partID); err == nil {
			pi.SetPart(refreshed)
		}
		if pi.onChanged != nil {
			pi.onChanged()
		}
	}, pi.win)
}

func (pi *PartInspector) handleEnrichMetadata() {
	if pi.part == nil {
		return
	}
	query := strings.TrimSpace(pi.lcscEntry.Text)
	if query == "" {
		query = strings.TrimSpace(pi.mpnEntry.Text)
	}
	if query == "" {
		query = pi.part.Name
	}

	pi.enrichBtn.Disable()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := importer.LookupMetadata(ctx, pi.mgr.Config(), query)
		fyne.Do(func() {
			pi.enrichBtn.Enable()
			if err != nil {
				dialog.ShowError(err, pi.win)
				return
			}
			for k, v := range res.Attributes {
				if pi.editMeta[k] == "" {
					pi.editMeta[k] = v
				}
			}
			if pi.mpnEntry.Text == "" && res.MPN != "" {
				pi.mpnEntry.SetText(res.MPN)
			}
			if pi.mfgEntry.Text == "" && res.Manufacturer != "" {
				pi.mfgEntry.SetText(res.Manufacturer)
			}
			if pi.lcscEntry.Text == "" && res.LCSCPart != "" {
				pi.lcscEntry.SetText(res.LCSCPart)
			}
			if pi.dsEntry.Text == "" && res.Datasheet != "" {
				pi.dsEntry.SetText(res.Datasheet)
			}
			if pi.descEntry.Text == "" && res.Description != "" {
				pi.descEntry.SetText(res.Description)
			}
			pi.rebuildMetadataList()
			dialog.ShowInformation("Metadata Enriched", fmt.Sprintf("Merged fields from %s. Click Save to commit.", res.Provider), pi.win)
		})
	}()
}

func copyStrMap(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
