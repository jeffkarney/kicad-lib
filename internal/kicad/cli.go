package kicad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// UpgradeSymbolLib runs `kicad-cli sym upgrade` to convert legacy KiCad v5 .lib files (or older .kicad_sym)
// into current-format `.kicad_sym` files.
func UpgradeSymbolLib(cliPath, inputLibPath, outputKicadSymPath string) error {
	if strings.TrimSpace(cliPath) == "" {
		return errors.New("kicad-cli path is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(outputKicadSymPath), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := cleanKiCadCLICommand(ctx, cliPath, "sym", "upgrade", "--force", "--output", outputKicadSymPath, inputLibPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kicad-cli sym upgrade %q: %w (%s)", inputLibPath, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// UpgradeFootprintDir runs `kicad-cli fp upgrade` on a footprint file or `.pretty` directory.
func UpgradeFootprintDir(cliPath, inputPath, outputDir string) error {
	if strings.TrimSpace(cliPath) == "" {
		return errors.New("kicad-cli path is not configured")
	}
	if outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{"fp", "upgrade", "--force"}
	if outputDir != "" {
		args = append(args, "--output", outputDir)
	}
	args = append(args, inputPath)

	cmd := cleanKiCadCLICommand(ctx, cliPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kicad-cli fp upgrade %q: %w (%s)", inputPath, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ExportSymbolSVG renders a single symbol from symLibPath into outDir via `kicad-cli sym export svg`
// and returns the resulting SVG file path.
func ExportSymbolSVG(cliPath, symLibPath, symbolName, outDir string) (string, error) {
	if strings.TrimSpace(cliPath) == "" {
		return "", errors.New("kicad-cli path is not configured")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	args := []string{"sym", "export", "svg", "--output", outDir}
	if symbolName != "" {
		args = append(args, "--symbol", symbolName)
	}
	args = append(args, symLibPath)

	cmd := cleanKiCadCLICommand(ctx, cliPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("kicad-cli sym export svg: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	return findFirstSVG(outDir, symbolName)
}

// ExportFootprintSVG renders a footprint from prettyDir (or single .kicad_mod) into outDir via `kicad-cli fp export svg`
// and returns the resulting SVG file path.
func ExportFootprintSVG(cliPath, prettyDirOrFile, footprintName, outDir string) (string, error) {
	if strings.TrimSpace(cliPath) == "" {
		return "", errors.New("kicad-cli path is not configured")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	args := []string{"fp", "export", "svg", "--output", outDir}
	if footprintName != "" {
		args = append(args, "--footprint", footprintName)
	}
	args = append(args, prettyDirOrFile)

	cmd := cleanKiCadCLICommand(ctx, cliPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("kicad-cli fp export svg: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	return findFirstSVG(outDir, footprintName)
}

func cleanKiCadCLICommand(ctx context.Context, cliPath string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, cliPath, args...)
	var cleanEnv []string
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "LD_LIBRARY_PATH=") {
			continue
		}
		cleanEnv = append(cleanEnv, env)
	}
	cmd.Env = cleanEnv
	return cmd
}

func findFirstSVG(dir, preferredPrefix string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var fallback string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".svg") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if preferredPrefix != "" && strings.Contains(strings.ToLower(e.Name()), strings.ToLower(preferredPrefix)) {
			return p, nil
		}
		if fallback == "" {
			fallback = p
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("no svg file produced in %s", dir)
}
