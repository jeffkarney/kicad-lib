# kicad-lib — KiCad Part Library Manager

A Go desktop application (built with [Fyne](https://fyne.io)) for managing KiCad part libraries with local vault storage, SQLite FTS5 metadata search, drag-and-drop archive import, KiCad library table synchronization, KiCad 9/10 IPC API integration, and Git / LCSC / distributor imports.

## Features

- **Drag-and-Drop Archive & Folder Importer**:
  - Drop `.zip` archives (including nested zips from UltraLibrarian, SnapMagic/SnapEDA, SamacSys, etc.) or folders directly onto the application window.
  - Automatically scans for schematic symbols (`.kicad_sym`, plus legacy KiCad v5 `.lib` upgraded via `kicad-cli`), footprints (`.kicad_mod`), 3D models (`.step`, `.stp`, `.stpz`, `.wrl`), and datasheets (`.pdf`).
  - Pairs symbols, footprints, and 3D models and prompts you to add them to an existing KiCad library or create a new library inline.
- **Canonical Vault & Library Regeneration**:
  - Copies all imported assets into an immutable local vault (`<StorageRoot>/vault/parts/<uuid>/`) backed by SQLite (`<StorageRoot>/kicad-lib.db`).
  - Generates clean synced KiCad libraries under `<StorageRoot>/libraries/` (`symbols/<Lib>.kicad_sym`, `footprints/<Lib>.pretty/`, `3dmodels/<Lib>.3dshapes/`).
  - Automatically links symbol `Footprint` properties (`<Lib>:<Footprint>`) and rewrites footprint `(model ...)` paths to the managed `.3dshapes` folder.
  - Regenerate any or all libraries from the database at any time, or export a standalone portable `.zip` archive using `${KIPRJMOD}` relative paths.
- **Move Parts Between Libraries**:
  - Move parts between libraries in the inspector; automatically updates symbol footprint references, moves footprint/3D files, and regenerates both source and destination libraries.
- **Searchable Metadata & FTS5**:
  - Add arbitrary custom key-value metadata fields (`Package`, `Voltage`, `MPN`, etc.) to any part.
  - Metadata is indexed in SQLite FTS5 for instant search and written into KiCad symbol `(property ...)` fields.
- **KiCad Integration (Tables, CLI & IPC API)**:
  - Registers generated libraries in KiCad's global `sym-lib-table` and `fp-lib-table` (creating `.bak` backups first).
  - Connects to KiCad 9/10's IPC API over NNG/Protobuf Unix sockets (`$KICAD_API_SOCKET` or `/tmp/kicad/api*.sock`) to inspect live KiCad state and request editor refreshes.
  - Uses `kicad-cli` to upgrade legacy `.lib` files and render SVG previews of symbols and footprints inside the inspector.
- **Git, LCSC / EasyEDA & Distributor Imports**:
  - Clone/pull Git repositories and selectively import discovered KiCad parts.
  - Import components by LCSC C-number (symbol, footprint, 3D STEP model, and parametric specs).
  - Enrich metadata from Mouser, DigiKey v4, Nexar/Octopart, or public LCSC/EasyEDA lookup.

## Build & Run

### NixOS / Nix Shell

```bash
nix-shell --run "go build -o bin/kicad-lib ./cmd/kicad-lib"
nix-shell --run "./bin/kicad-lib"
```

### Standard Linux (with X11 & OpenGL dev headers installed)

```bash
go build -o bin/kicad-lib ./cmd/kicad-lib
./bin/kicad-lib
```

## Tests

```bash
nix-shell --run "go test -v ./..."
```
