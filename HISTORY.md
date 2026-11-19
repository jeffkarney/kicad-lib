# Project History & Architecture Summary (`kicad-lib`)

**Last Updated:** 2026-11-18
**Repository:** `/home/jeffk/Documents/Projects/kicad-lib`
**Module:** `github.com/jeffk/kicad-lib` (Go `1.26.0`)

---

## 1. Project Overview

`kicad-lib` is a Go desktop application for managing KiCad (v9/v10+) component libraries. It uses **Fyne (`fyne.io/fyne/v2 v2.8.2`)** for the native GUI and pure-Go **SQLite (`modernc.org/sqlite v1.62.0`)** with **FTS5** full-text indexing.

Core capabilities:
- **Drag-and-Drop / Picker Ingestion**: Accepts `.zip` archives (including nested vendor zips from UltraLibrarian, SnapMagic/SnapEDA, SamacSys, EasyEDA), folders, legacy `.lib` symbol files, standalone `.kicad_sym` / `.kicad_mod` / 3D models, and full KiCad projects/boards (`.kicad_pro`, `.kicad_sch`, `.kicad_pcb`).
- **Canonical Vault & KiCad Library Sync**: Stores raw assets canonically per part UUID under `<StorageRoot>/vault/parts/<uuid>/`, persists metadata and S-expressions in `<StorageRoot>/kicad-lib.db`, generates synced `.kicad_sym` / `.pretty` / `.3dshapes` directories under `<StorageRoot>/libraries/`, and registers them in KiCad's global `sym-lib-table` and `fp-lib-table` (with `.bak` backups).
- **KiCad 9/10 IPC API Integration**: Connects over NNG (`go.nanomsg.org/mangos/v3`) + Protobuf wire encoding (`google.golang.org/protobuf/encoding/protowire`) to `$KICAD_API_SOCKET` / `/tmp/kicad/api*.sock`, pulls and syncs existing global/project KiCad libraries (`SyncFromKiCad`), and triggers editor refreshes.
- **Interactive 2D & 3D Previews**: Pure-Go software rasterizer previews schematic symbols, PCB footprints (without requiring SVGs), and `.step` / `.stp` / `.stpZ` / `.wrl` 3D models with mouse-wheel zoom, drag pan/orbit, camera presets, and full-window expansion.
- **Online Importers & Metadata Enrichment**: Clones/pulls Git repositories (`ScanGitRepo`), fetches parts by LCSC C-number (`FetchLCSCPart`), and enriches component metadata via Mouser, DigiKey v4, Nexar/Octopart, or EasyEDA public search (`LookupMetadata`).
- **Duplicate Detection & Selective Merge**: Detects duplicate parts by Name, MPN, or LCSC ID (`FindDuplicateParts`), alerts the user during drop staging, shows a side-by-side comparison modal (fields, metadata diff, and interactive 2D/3D previews), and selectively merges, overwrites, renames, or skips duplicates (`MergePart`).

---

## 2. Build, Test, and Run Environment (NixOS)

All builds, tests, and runs on NixOS must run inside `nix-shell` using `shell.nix` so CGO links against the host-compatible `nixpkgs` flake libraries (`libGL`, `libglvnd`, `wayland`, `libxkbcommon`, X11 headers) with `-Wl,-rpath,/run/opengl-driver/lib`.

```bash
# Run full test suite (all packages)
nix-shell --run "go test -v ./..."

# Build binary
nix-shell --run "go build -o bin/kicad-lib ./cmd/kicad-lib"

# Run GUI application
nix-shell --run "./bin/kicad-lib"
```

---

## 3. Architecture & Package Map

### Entry Point & Config
- `cmd/kicad-lib/main.go`: Loads config, initializes storage dirs, opens SQLite DB, creates `kicad.IPCClient` and `storage.Manager`, and starts `ui.NewApp(mgr)`.
- `internal/config/config.go`: Manages `~/.config/kicad-lib/config.json` and defaults (`StorageRoot` -> `~/.local/share/kicad-lib`, auto-detected `KiCadConfigDir` `10.0`/`9.0`/`8.0`/`7.0`, `kicad-cli` on `PATH`, `$KICAD_API_SOCKET` / `/tmp/kicad/api.sock`, API keys for Mouser/DigiKey/Nexar).
  - Storage layout under `StorageRoot`:
    - `kicad-lib.db` (`DBPath`)
    - `vault/parts/<uuid>/` (`VaultDir`)
    - `libraries/{symbols,footprints,3dmodels}/` (`LibrariesDir`)
    - `cache/{git,previews}/` (`GitCacheDir`, `PreviewCacheDir`)

### Database Layer (`internal/db/`)
- `models.go`: Structs `Library`, `Part`, `Symbol`, `Footprint`, `Model3D`, `SearchFilter`.
- `db.go`:
  - Schema tables: `libraries` (with `source_uri`, `fp_source_uri`, `source_mtime`), `parts` (`UNIQUE(library_id, name)`), `symbols`, `footprints`, `models_3d`, `part_metadata`, and virtual table `parts_fts` (`FTS5`).
  - `BulkSyncLibrary`: Transactionally syncs external KiCad libraries (`source_type = 'kicad_sync'`) while preserving user-added/modified parts in the same library.
  - `hydratePartRelations`: Loads `Symbol`, `Footprint`, `Models3D`, and `Metadata`, lazy-loading `RawSExpr` from `VaultPath` when needed.
  - `FindDuplicateParts(name, mpn, lcscPart string, targetLibraryID int64)`: Case-insensitive duplicate lookup by Part Name, MPN, or LCSC Part ID, prioritizing matches in `targetLibraryID`.
- `search.go`: `SearchParts` joins `parts`, `libraries`, `symbols`, `footprints`, `parts_fts`, and `part_metadata` for fast browsing without eagerly loading `raw_sexpr`.
- `db_test.go`: `TestDBLifecycleAndFTS5Search`.

### KiCad File, IPC & Rendering Layer (`internal/kicad/`)
- `sexpr.go`: Full S-expression AST (`Node`, `ParseSExpr`, `ParseAllSExpr`, `PrettyString`, `FindProperty`, `SetOrAddProperty`, `Clone`).
- `symbol.go`: `ParseSymbolLibrary`, `PrepareSymbolNode`, `BuildSymbolLibraryFile`, `ParseSymbolLibraryFast` (fast streaming scanner avoiding full AST construction for large stock symbol libraries), `ExtractSymbolSExprFromFile`.
- `footprint.go`: `ParseFootprintFile`, `RewriteFootprint` (updates footprint name and managed `(model ...)` paths while preserving existing offset/scale/rotate transforms).
- `project_extract.go`:
  - `ExtractFromSchematic`: Parses `.kicad_sch`, extracts `(lib_symbols ...)` definitions, strips `<Lib>:` prefixes (and sub-unit prefixes), filters power/flag symbols (`power:*` / `(power)`) unless only power symbols exist, and merges missing properties (`Footprint`, `Datasheet`, `Description`, `ki_keywords`, `MPN`, `Manufacturer`, `LCSC`, custom metadata) from placed schematic `(symbol (lib_id ...) ...)` instances.
  - `ExtractSchematicSheetFiles`: Returns hierarchical `(sheet ... (property "Sheetfile" ...))` sub-sheet paths.
  - `ExtractFromPCB`: Parses `.kicad_pcb`, selects the best placement instance per footprint name (preferring `F.Cu`, `rot == 0`), strips board-instance attributes (`at`, `locked`, `uuid`, `tstamp`, `path`, `sheetname`, `sheetfile`, pad `net`/`pinfunction`/`pintype`), flips `B.*` layers + removes `(justify mirror)` on back-mounted footprints, and subtracts board placement rotation from pad/text angles to yield valid `.kicad_mod` S-expressions.
- `libtable.go`: `RegisterSymbolLibrary`, `RegisterFootprintLibrary`, `UnregisterLibrary`, `ResolveKiCadVariables` (checks user 3rdparty/3dmodels dirs, NixOS `kicad-cli` wrapper exports, `kicad_common.json`, live IPC `GetPaths`, and `os.Environ()`), `ExpandKiCadURI`, `LoadEffectiveLibTables`.
- `cli.go`: Wraps `kicad-cli` (`UpgradeSymbolLib`, `UpgradeFootprintDir`, `ExportSymbolSVG`, `ExportFootprintSVG`).
- `ipc.go`: Pure-Go KiCad 9/10 IPC client over NNG `req` + Unix IPC transport + `protowire` (`CheckStatus`, `DiscoverSocket`, `GetVersion`, `GetOpenDocuments`, `GetPaths`, `RefreshLibrariesAndNotify`).
- `render2d.go`: Parses symbol/footprint S-expressions (`ParseFootprintDrawing`, `ParseSymbolDrawing`, `ParseSymbolDrawingFromFile` with `(extends ...)` resolution) and rasterizes `Drawing2D` with zoom/pan (`RenderDrawing2D`).
- `render3d.go`: `Resolve3DModelPath` (probes sibling `.stpZ`/`.stpz`/`.step`/`.stp`/`.wrl` extensions) and `LoadMesh3D` + `RenderMesh3D` supporting ASCII STEP geometry (including face/item styling & cylindrical/toroidal 3D strip bridging), compressed `.stpZ` (both ZIP and gzip), and VRML 2.0 `.wrl` (`IndexedFaceSet`, `Coordinate`, `DEF`/`USE` materials, nested `Transform` hierarchy).
- `kicad_test.go`: 7 unit tests covering symbols, footprints, lib tables, mock IPC socket, fast parser & variable expansion, 2D rendering, and 3D STEP/WRL rendering.

### Storage & Sync Orchestrator (`internal/storage/`)
- `manager.go`: Coordinates SQLite + vault + generated KiCad libraries + IPC notifications:
  - `ImportPart`: Copies assets to vault, links `<Lib>:<Footprint>`, rewrites footprint 3D model paths, regenerates target library, and registers tables.
  - `MergePart(existingPartID, req, opts)`: Resolves duplicates via `MergeOptions.Strategy` (`"merge"`, `"overwrite"`, `"rename"`, `"skip"`), materializing `kicad_sync` parts into the vault before in-place modification and allowing independent selection of Details, Symbol, Footprint, 3D Models (`"combine"`, `"keep_existing"`, `"use_incoming"`), and Metadata (`"prefer_incoming"`, `"keep_existing"`, `"replace"`).
  - `MovePart`, `UpdatePart`, `DeletePart`, `Attach3DModel`, `RegenerateLibrary`, `RegenerateAllLibraries`, `ExportLibraryArchive`, `RenderPartPreviews`, `SyncFromKiCad`.
- `manager_test.go`: `TestStorageLifecycleMoveSearchRegenerateAndExport`, `TestSyncFromKiCadExistingLibraries`, `TestDuplicateDetectionAndMergePart`.

### Importers (`internal/importer/`)
- `archive.go`: `ScanDroppedPaths` and `ScanDirectory` (recursive zip extraction up to depth 3, `.kicad_pro` directory auto-expansion, hierarchical `.kicad_sch` sheet traversal, `.kicad_pcb` extraction with `${KIPRJMOD}` resolution using original dropped dirs, legacy `.lib` v5 upgrade via `kicad-cli` or pure-Go fallback converter, symbol/footprint/3D model pairing, and standalone PCB footprint emission).
- `git.go`: `CloneOrPullRepo`, `ScanGitRepo`.
- `lcsc.go`: `FetchLCSCPart` via EasyEDA component API (generates `.kicad_sym`, `.kicad_mod`, and fetches 3D `.step` when available).
- `distributor.go`: `LookupMetadata` across `MouserProvider`, `DigiKeyProvider` (OAuth2 v4), `NexarProvider` (GraphQL), and `EasyEDAPublicProvider` fallback.
- `archive_test.go`: 5 tests including `TestScanKiCadProjectSchematicAndPCB` (verified against both synthetic projects and the system KiCad `Arduino_Nano` template if present).

### Desktop GUI (`internal/ui/`)
- `app.go`: Main window (`1320x820`), drop handler (`SetOnDropped`), toolbar (`Import Zip/File`, `Import Folder`, `Git Repo`, `LCSC`, `New Library`, `Regenerate All`, `Export .zip`, `Sync IPC`, `Settings`), library sidebar, FTS5/metadata/Has-3D filterable part table, status bar, periodic IPC polling.
- `part_detail.go`: `PartInspector` with `Assets` (interactive 2D Symbol, 2D Footprint, and 3D Model previews + 3D model selector & attachment), `Details`, and `Metadata` (with LCSC/distributor enrichment) tabs, Move Library, Delete Part.
- `preview_widget.go`: `InteractivePreviewWidget` (`Scrolled` zoom, `Dragged` 2D pan / 3D orbit+pan), `NewPreviewPanel` toolbar (`ZoomIn`, `ZoomOut`, `Fit`, `Orbit`/`Pan`/`Iso`/`Top`, `Expand`), and `ShowFullWindowPreview` dialog.
- `drop_dialog.go`: `ShowDropStagingDialog` with candidate list, target library selector / inline creation, inline metadata editing/lookup, automatic duplicate detection, warning banner, per-row duplicate/resolution badges, and sequential unresolved-duplicate resolution flow on Import.
- `duplicate_dialog.go`: `ShowDuplicateCompareDialog` providing side-by-side Existing vs. Incoming part field comparison (`[DIFF]` highlights), full Metadata Diff table, side-by-side interactive 2D/3D previews, and Merge/Overwrite/Rename/Skip controls.
- `import_online.go`: `ShowGitImportDialog`, `ShowLCSCImportDialog`.
- `settings.go`: `ShowSettingsDialog`.
- `ui_test.go`: Headless Fyne tests (`TestPartInspectorHeadless`, `TestInteractivePreviewWidgetZoomPanAndOrbit`, `TestDuplicateCompareDialogHeadless`).

---

## 4. Chronological Development & Bugfix Log

1. **Initial Architecture & Core App**:
   - Implemented SQLite schema + FTS5, immutable vault + library regeneration (`internal/storage`), S-expression library/footprint manipulation, `kicad-cli` + NNG IPC client (`internal/kicad`), archive/Git/LCSC/distributor importers (`internal/importer`), and Fyne desktop UI (`internal/ui`).
2. **NixOS OpenGL/EGL Window Creation Fix**:
   - *Issue*: `APIUnavailable: EGL: Failed to get EGL display: Success` on launch when `shell.nix` evaluated an older `<nixpkgs>` channel (`glibc-2.42`) while host `/run/opengl-driver/lib/libgallium-26.3.0.so` required `GLIBC_2.43`.
   - *Fix*: Updated `shell.nix` to default `pkgs` to `(builtins.getFlake "nixpkgs").legacyPackages.${builtins.currentSystem}` and exported `CGO_LDFLAGS="-Wl,-rpath,/run/opengl-driver/lib"` + `LD_LIBRARY_PATH` including `/run/opengl-driver/lib`. Added `wayland` and `libxkbcommon` for `glfw/v3.4`.
3. **Automatic Sync of Existing KiCad Libraries (`SyncFromKiCad`)**:
   - Added `LoadEffectiveLibTables`, `ResolveKiCadVariables` (including NixOS wrapper export parsing and IPC `GetPaths`), `ParseSymbolLibraryFast`, and `BulkSyncLibrary` so connecting to KiCad IPC / startup pulls all stock and 3rd-party libraries without overwriting KiCad's global tables unless a library has user-managed parts.
   - *Bugfix*: Removed `lib.PartCount > 0` from `IsLibrarySyncUpToDate` / `BulkSyncLibrary` so moving the last synced part out of a synced library doesn't re-import it on subsequent syncs; also fixed `db.Open` to `os.MkdirAll(filepath.Dir(dbPath), 0o755)`.
4. **Interactive 2D & 3D Previews (Zoom, Pan/Orbit, Full-Window)**:
   - Built pure-Go vector 2D renderer (`render2d.go`), pure-Go 3D renderer (`render3d.go`), and `InteractivePreviewWidget` / `ShowFullWindowPreview` (`preview_widget.go`).
   - *Finding*: `kicad-cli pcb render` segfaults in KiCad 10.0.8's `S3D_CACHE`, so 3D previews are rendered completely in Go (`render3d.go`). Also fixed `RenderPartPreviews` to pass `.pretty` directories (never `.kicad_mod` file paths) to `kicad-cli fp export svg`.
5. **`.stpZ` and `.wrl` 3D Model Support**:
   - Added ZIP (`PK\x03\x04`) in addition to gzip extraction in `LoadMesh3D` (KiCad `.stpZ` files are ZIP-compressed STEP files).
   - Rewrote `parseVRMLMesh` to scope points/indices/materials per `Shape` (fixing footprints/models where `coordIndex` precedes `point` inside `IndexedFaceSet` and supporting `DEF`/`USE` materials).
   - Added `Resolve3DModelPath` for sibling extension fallback (when a footprint references `.wrl` but KiCad 10 stores `.stpZ` on disk) and strip triangulation for 360-degree cylindrical/toroidal STEP faces.
6. **KiCad Project/Schematic/PCB Extraction & Duplicate Compare/Merge**:
   - Added `internal/kicad/project_extract.go` and updated `internal/importer/archive.go` to extract symbols (`.kicad_sch`) and normalized footprints + resolved 3D models (`.kicad_pcb`) when dropping `.kicad_pro`, `.kicad_sch`, `.kicad_pcb`, folders, or zips.
   - Added `db.FindDuplicateParts`, `storage.MergePart` / `MergeOptions`, `ui.ShowDuplicateCompareDialog`, and duplicate notification/resolution workflows in `ui.ShowDropStagingDialog`.

---

## 5. Key Invariants & Gotchas for Future Changes

- **Never overwrite external KiCad lib tables**: `RegenerateLibrary` / `RegenerateAllLibraries` only writes `sym-lib-table` / `fp-lib-table` when `lib.SourceURI == ""` OR the library contains non-`kicad_sync` parts. Be cautious if merging directly into a stock `kicad_sync` library (`Device`, etc.) — `hasManagedParts` becomes true if any part in that library stops being `kicad_sync`.
- **SQLite single connection (`SetMaxOpenConns(1)`) & FTS5 cleanup**: Because `parts_fts` is an FTS5 table without foreign-key triggers, `DeleteLibrary`, `DeletePart`, `BulkSyncLibrary`, and `SavePart` must keep `parts_fts` synchronized manually inside the transaction.
- **3D model paths & KiCad 10**: Always normalize/probe via `kicad.Resolve3DModelPath` and expand variables via `kicad.ExpandKiCadURI`. Do not shell out to `kicad-cli pcb render`.
- **Tests run in `nix-shell`**: Headless Fyne UI tests use `fyne.io/fyne/v2/test`; all packages (`internal/db`, `internal/importer`, `internal/kicad`, `internal/storage`, `internal/ui`) pass cleanly under `nix-shell --run "go test -v ./..."`.
