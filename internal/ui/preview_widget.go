package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jeffk/kicad-lib/internal/kicad"
)

// PreviewMode identifies what kind of scene an InteractivePreviewWidget displays.
type PreviewMode int

const (
	PreviewModeEmpty PreviewMode = iota
	PreviewMode2D
	PreviewMode3D
)

// InteractivePreviewWidget renders 2D symbols/footprints or 3D STEP/WRL models
// with mouse-wheel zoom, drag pan/orbit, and full-window expansion support.
type InteractivePreviewWidget struct {
	widget.BaseWidget

	mu         sync.Mutex
	mode       PreviewMode
	emptyMsg   string
	drawing2D  *kicad.Drawing2D
	view2D     kicad.View2D
	mesh3D     *kicad.Mesh3D
	cam3D      kicad.Camera3D
	dragMode3D string // "Orbit" or "Pan"
	minSize    fyne.Size

	raster   *canvas.Raster
	infoLbl  *widget.Label
	onChange func()
}

// NewInteractivePreviewWidget creates an interactive preview canvas with zoom/pan/orbit support.
func NewInteractivePreviewWidget(mode PreviewMode, minSize fyne.Size) *InteractivePreviewWidget {
	p := &InteractivePreviewWidget{
		mode:       mode,
		emptyMsg:   "No preview available",
		view2D:     kicad.DefaultView2D(),
		cam3D:      kicad.DefaultCamera3D(),
		dragMode3D: "Orbit",
		minSize:    minSize,
	}
	p.raster = canvas.NewRaster(p.renderFrame)
	p.raster.ScaleMode = canvas.ImageScaleFastest
	p.infoLbl = widget.NewLabel(p.emptyMsg)
	p.infoLbl.Alignment = fyne.TextAlignCenter
	p.infoLbl.Wrapping = fyne.TextWrapWord
	p.ExtendBaseWidget(p)
	return p
}

// Mode returns the active preview mode.
func (p *InteractivePreviewWidget) Mode() PreviewMode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode
}

// SetMinSize sets the widget's minimum viewport size.
func (p *InteractivePreviewWidget) SetMinSize(sz fyne.Size) {
	p.mu.Lock()
	p.minSize = sz
	p.mu.Unlock()
	p.Refresh()
}

// SetEmpty switches the preview to an informational empty/loading state.
func (p *InteractivePreviewWidget) SetEmpty(msg string) {
	p.mu.Lock()
	p.mode = PreviewModeEmpty
	p.emptyMsg = msg
	p.drawing2D = nil
	p.mesh3D = nil
	p.mu.Unlock()
	p.infoLbl.SetText(msg)
	p.infoLbl.Show()
	p.Refresh()
}

// SetDrawing2D loads a 2D footprint or symbol vector scene and resets view to fit.
func (p *InteractivePreviewWidget) SetDrawing2D(d *kicad.Drawing2D) {
	if d == nil {
		p.SetEmpty("No 2D geometry to display")
		return
	}
	p.mu.Lock()
	p.mode = PreviewMode2D
	p.drawing2D = d
	p.view2D = kicad.DefaultView2D()
	p.mesh3D = nil
	p.mu.Unlock()
	p.infoLbl.Hide()
	p.Refresh()
}

// SetMesh3D loads a 3D STEP/VRML mesh and resets the 3D camera to isometric view.
func (p *InteractivePreviewWidget) SetMesh3D(m *kicad.Mesh3D) {
	if m == nil {
		p.SetEmpty("No 3D model geometry")
		return
	}
	p.mu.Lock()
	p.mode = PreviewMode3D
	p.mesh3D = m
	p.cam3D = kicad.DefaultCamera3D()
	p.drawing2D = nil
	p.mu.Unlock()
	p.infoLbl.Hide()
	p.Refresh()
}

// SetDragMode3D sets whether dragging orbits ("Orbit") or pans ("Pan") in 3D mode.
func (p *InteractivePreviewWidget) SetDragMode3D(mode string) {
	p.mu.Lock()
	if mode == "Pan" || mode == "Orbit" {
		p.dragMode3D = mode
	}
	p.mu.Unlock()
}

// SetCameraPreset applies a standard 3D viewing angle ("Iso", "Top", "Front", "Side").
func (p *InteractivePreviewWidget) SetCameraPreset(preset string) {
	p.mu.Lock()
	switch preset {
	case "Top":
		p.cam3D.Yaw = 0
		p.cam3D.Pitch = 0
	case "Front":
		p.cam3D.Yaw = 0
		p.cam3D.Pitch = 90
	case "Side":
		p.cam3D.Yaw = 90
		p.cam3D.Pitch = 90
	default: // Iso
		p.cam3D.Yaw = -35
		p.cam3D.Pitch = 55
	}
	p.cam3D.PanX = 0
	p.cam3D.PanY = 0
	p.mu.Unlock()
	p.Refresh()
}

// ZoomBy multiplies the current zoom level by factor.
func (p *InteractivePreviewWidget) ZoomBy(factor float64) {
	p.mu.Lock()
	switch p.mode {
	case PreviewMode2D:
		p.view2D.Zoom = clampZoom(p.view2D.Zoom * factor)
	case PreviewMode3D:
		p.cam3D.Zoom = clampZoom(p.cam3D.Zoom * factor)
	}
	p.mu.Unlock()
	p.Refresh()
}

// ResetView restores default zoom, pan, and 3D camera orientation.
func (p *InteractivePreviewWidget) ResetView() {
	p.mu.Lock()
	switch p.mode {
	case PreviewMode2D:
		p.view2D = kicad.DefaultView2D()
	case PreviewMode3D:
		p.cam3D = kicad.DefaultCamera3D()
	}
	p.mu.Unlock()
	p.Refresh()
}

// CloneTo copies the loaded scene and current view/camera state to another InteractivePreviewWidget.
func (p *InteractivePreviewWidget) CloneTo(dst *InteractivePreviewWidget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dst.mu.Lock()
	defer dst.mu.Unlock()

	dst.mode = p.mode
	dst.emptyMsg = p.emptyMsg
	dst.drawing2D = p.drawing2D
	dst.view2D = p.view2D
	dst.mesh3D = p.mesh3D
	dst.cam3D = p.cam3D
	dst.dragMode3D = p.dragMode3D
}

// Scrolled handles mouse-wheel zooming (fyne.Scrollable).
func (p *InteractivePreviewWidget) Scrolled(ev *fyne.ScrollEvent) {
	dy := ev.Scrolled.DY
	if dy == 0 {
		return
	}
	factor := 1.15
	if dy < 0 {
		factor = 1.0 / 1.15
	}
	p.ZoomBy(factor)
}

// Dragged handles mouse-drag panning in 2D and orbit/pan in 3D (fyne.Draggable).
func (p *InteractivePreviewWidget) Dragged(ev *fyne.DragEvent) {
	p.mu.Lock()
	switch p.mode {
	case PreviewMode2D:
		p.view2D.PanX += float64(ev.Dragged.DX)
		p.view2D.PanY += float64(ev.Dragged.DY)
	case PreviewMode3D:
		if p.dragMode3D == "Pan" {
			p.cam3D.PanX += float64(ev.Dragged.DX)
			p.cam3D.PanY += float64(ev.Dragged.DY)
		} else {
			p.cam3D.Yaw += float64(ev.Dragged.DX) * 0.6
			p.cam3D.Pitch = clampPitch(p.cam3D.Pitch + float64(ev.Dragged.DY)*0.6)
		}
	}
	p.mu.Unlock()
	p.Refresh()
}

// DragEnd completes a drag gesture (fyne.Draggable).
func (p *InteractivePreviewWidget) DragEnd() {}

// CreateRenderer builds the Fyne widget renderer.
func (p *InteractivePreviewWidget) CreateRenderer() fyne.WidgetRenderer {
	stack := container.NewStack(p.raster, container.NewCenter(p.infoLbl))
	return &previewWidgetRenderer{preview: p, content: stack}
}

type previewWidgetRenderer struct {
	preview *InteractivePreviewWidget
	content *fyne.Container
}

func (r *previewWidgetRenderer) Layout(sz fyne.Size) {
	r.content.Resize(sz)
}

func (r *previewWidgetRenderer) MinSize() fyne.Size {
	r.preview.mu.Lock()
	ms := r.preview.minSize
	r.preview.mu.Unlock()
	return ms
}

func (r *previewWidgetRenderer) Refresh() {
	r.preview.raster.Refresh()
	r.content.Refresh()
}

func (r *previewWidgetRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.content}
}

func (r *previewWidgetRenderer) Destroy() {}

func (p *InteractivePreviewWidget) renderFrame(w, h int) image.Image {
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	// Cap internal software raster resolution to keep drag/zoom silky smooth on HiDPI displays
	const maxDim = 960
	rw, rh := w, h
	scaleRatio := 1.0
	if rw > maxDim || rh > maxDim {
		sx := float64(maxDim) / float64(rw)
		sy := float64(maxDim) / float64(rh)
		scaleRatio = math.Min(sx, sy)
		rw = max(1, int(math.Round(float64(rw)*scaleRatio)))
		rh = max(1, int(math.Round(float64(rh)*scaleRatio)))
	}

	// Scale pan from widget DIP coordinates into raster coordinates
	sz := p.Size()
	panScaleX, panScaleY := 1.0, 1.0
	if sz.Width > 1 {
		panScaleX = float64(rw) / float64(sz.Width)
	}
	if sz.Height > 1 {
		panScaleY = float64(rh) / float64(sz.Height)
	}

	p.mu.Lock()
	mode := p.mode
	d2 := p.drawing2D
	v2 := p.view2D
	m3 := p.mesh3D
	c3 := p.cam3D
	p.mu.Unlock()

	switch mode {
	case PreviewMode2D:
		v2.PanX *= panScaleX
		v2.PanY *= panScaleY
		return kicad.RenderDrawing2D(d2, rw, rh, v2)
	case PreviewMode3D:
		c3.PanX *= panScaleX
		c3.PanY *= panScaleY
		return kicad.RenderMesh3D(m3, rw, rh, c3)
	default:
		img := image.NewRGBA(image.Rect(0, 0, rw, rh))
		bg := color.RGBA{0x16, 0x1B, 0x22, 0xFF}
		for y := 0; y < rh; y++ {
			for x := 0; x < rw; x++ {
				img.SetRGBA(x, y, bg)
			}
		}
		return img
	}
}

// NewPreviewPanel wraps an InteractivePreviewWidget with zoom, reset, optional 3D camera controls,
// and an Expand-to-Full-Window button.
func NewPreviewPanel(win fyne.Window, title string, preview *InteractivePreviewWidget, allow3DControls bool, extraControls ...fyne.CanvasObject) *fyne.Container {
	zoomInBtn := widget.NewButtonWithIcon("", theme.ZoomInIcon(), func() {
		preview.ZoomBy(1.25)
	})
	zoomOutBtn := widget.NewButtonWithIcon("", theme.ZoomOutIcon(), func() {
		preview.ZoomBy(1.0 / 1.25)
	})
	fitBtn := widget.NewButtonWithIcon("Fit", theme.ZoomFitIcon(), func() {
		preview.ResetView()
	})
	expandBtn := widget.NewButtonWithIcon("Expand", theme.ViewFullScreenIcon(), func() {
		ShowFullWindowPreview(win, title, preview, allow3DControls)
	})

	controls := []fyne.CanvasObject{zoomOutBtn, zoomInBtn, fitBtn}
	if allow3DControls {
		orbitBtn := widget.NewButton("Orbit", nil)
		panBtn := widget.NewButton("Pan", nil)
		orbitBtn.Importance = widget.HighImportance
		orbitBtn.OnTapped = func() {
			preview.SetDragMode3D("Orbit")
			orbitBtn.Importance = widget.HighImportance
			panBtn.Importance = widget.MediumImportance
			orbitBtn.Refresh()
			panBtn.Refresh()
		}
		panBtn.OnTapped = func() {
			preview.SetDragMode3D("Pan")
			panBtn.Importance = widget.HighImportance
			orbitBtn.Importance = widget.MediumImportance
			orbitBtn.Refresh()
			panBtn.Refresh()
		}
		isoBtn := widget.NewButton("Iso", func() { preview.SetCameraPreset("Iso") })
		topBtn := widget.NewButton("Top", func() { preview.SetCameraPreset("Top") })
		controls = append(controls, orbitBtn, panBtn, isoBtn, topBtn)
	}
	controls = append(controls, layout.NewSpacer())
	controls = append(controls, extraControls...)
	controls = append(controls, expandBtn)

	bar := container.NewHBox(controls...)
	return container.NewBorder(bar, nil, nil, nil, preview)
}

// ShowFullWindowPreview opens an interactive preview expanded to fill the application window.
func ShowFullWindowPreview(win fyne.Window, title string, source *InteractivePreviewWidget, allow3DControls bool) {
	if win == nil || source == nil {
		return
	}
	fullPreview := NewInteractivePreviewWidget(source.Mode(), fyne.NewSize(640, 440))
	source.CloneTo(fullPreview)
	if fullPreview.Mode() == PreviewModeEmpty {
		fullPreview.SetEmpty("No preview geometry available")
	} else {
		fullPreview.infoLbl.Hide()
	}

	zoomInBtn := widget.NewButtonWithIcon("Zoom In", theme.ZoomInIcon(), func() { fullPreview.ZoomBy(1.25) })
	zoomOutBtn := widget.NewButtonWithIcon("Zoom Out", theme.ZoomOutIcon(), func() { fullPreview.ZoomBy(1.0 / 1.25) })
	fitBtn := widget.NewButtonWithIcon("Fit", theme.ZoomFitIcon(), func() { fullPreview.ResetView() })

	barItems := []fyne.CanvasObject{zoomOutBtn, zoomInBtn, fitBtn}
	if allow3DControls || fullPreview.Mode() == PreviewMode3D {
		dragMode := widget.NewRadioGroup([]string{"Orbit", "Pan"}, func(sel string) {
			fullPreview.SetDragMode3D(sel)
		})
		dragMode.Horizontal = true
		dragMode.SetSelected("Orbit")

		isoBtn := widget.NewButton("Iso", func() { fullPreview.SetCameraPreset("Iso") })
		topBtn := widget.NewButton("Top", func() { fullPreview.SetCameraPreset("Top") })
		frontBtn := widget.NewButton("Front", func() { fullPreview.SetCameraPreset("Front") })
		sideBtn := widget.NewButton("Side", func() { fullPreview.SetCameraPreset("Side") })
		barItems = append(barItems, widget.NewSeparator(), dragMode, isoBtn, topBtn, frontBtn, sideBtn)
	}

	hint := widget.NewLabel("Scroll: Zoom  |  Drag: Pan / Orbit")
	if fullPreview.Mode() == PreviewMode2D {
		hint.SetText("Scroll: Zoom  |  Drag: Pan")
	}

	var dlg dialog.Dialog
	closeBtn := widget.NewButtonWithIcon("Close", theme.CancelIcon(), func() {
		dlg.Hide()
	})
	barItems = append(barItems, layout.NewSpacer(), hint, closeBtn)

	body := container.NewBorder(
		container.NewHBox(barItems...),
		nil, nil, nil,
		fullPreview,
	)

	dlg = dialog.NewCustomWithoutButtons(fmt.Sprintf("%s — Full Window Preview", title), body, win)
	dlg.SetOnClosed(func() {
		fullPreview.CloneTo(source)
		source.Refresh()
	})

	winSize := win.Canvas().Size()
	w := float32(math.Max(760, float64(winSize.Width)*0.95))
	h := float32(math.Max(540, float64(winSize.Height)*0.92))
	dlg.Resize(fyne.NewSize(w, h))
	dlg.Show()
}

func clampZoom(z float64) float64 {
	if z < 0.1 {
		return 0.1
	}
	if z > 40.0 {
		return 40.0
	}
	return z
}

func clampPitch(p float64) float64 {
	if p < -180 {
		p += 360
	}
	if p > 180 {
		p -= 360
	}
	return p
}
