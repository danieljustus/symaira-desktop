package main

import (
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

type storePathDocument struct {
	SchemaVersion int              `json:"schema_version"`
	Complete      bool             `json:"complete"`
	Oracle        inventory.Oracle `json:"oracle"`
	GOOS          string           `json:"goos"`
	GOARCH        string           `json:"goarch"`
	GoVersion     string           `json:"go_version"`
	Cases         []storePathCase  `json:"cases"`
	ownedRoot     bool
}

type storePathCase struct {
	ID          string             `json:"id"`
	Root        string             `json:"root"`
	Environment map[string]string  `json:"environment"`
	Contacts    [4]string          `json:"contacts"`
	Ingest      []ingestPathResult `json:"ingest"`
	Before      map[string]string  `json:"before"`
	After       map[string]string  `json:"after"`
}

type ingestPathResult struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Error string `json:"error"`
}

// Each native host executes the production resolvers. No Darwin expectations
// are reused as Windows semantics, including backslashes, drive names and HOME.
func buildStorePaths(oracle inventory.Oracle, root string) (storePathDocument, error) {
	value := storePathDocument{SchemaVersion: 1, Oracle: oracle, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version()}
	if err := os.Mkdir(root, 0o700); err != nil {
		return value, fmt.Errorf("capture root must be fresh: %w", err)
	}
	value.ownedRoot = true
	cwd, err := os.Getwd()
	if err != nil {
		return value, err
	}
	for _, id := range []string{
		"fresh", "legacy-files", "both-files", "primary-directory", "legacy-directory",
		"mixed-archive", "contacts-overrides", "padded-overrides", "blank-overrides",
		"home-defaults", "no-home", "xdg-without-home", "relative-xdg", "lexical-xdg", "different-home-profile",
	} {
		caseRoot := filepath.Join(root, id)
		env := map[string]string{
			"HOME": filepath.Join(caseRoot, "home"), "USERPROFILE": filepath.Join(caseRoot, "home"),
			"XDG_DATA_HOME": filepath.Join(caseRoot, "data"), "XDG_CONFIG_HOME": filepath.Join(caseRoot, "config"), "XDG_CACHE_HOME": filepath.Join(caseRoot, "cache"),
		}
		for _, directory := range []string{"home", "data", "config", "cache"} {
			if err := os.MkdirAll(filepath.Join(caseRoot, directory), 0o700); err != nil {
				return value, err
			}
		}
		seed := map[string]bool{}
		switch id {
		case "legacy-files", "legacy-directory", "both-files", "primary-directory":
			for _, rel := range []string{"data/symrelate/symrelate.db", "data/symingest/symingest.db", "data/symingest/archive"} {
				seed[rel] = id == "legacy-directory" || rel == "data/symingest/archive"
			}
			if id == "both-files" || id == "primary-directory" {
				for _, rel := range []string{"data/symdesk/symrelate.db", "data/symdesk/symingest.db", "data/symdesk/archive"} {
					seed[rel] = id == "primary-directory" || rel == "data/symdesk/archive"
				}
			}
		case "mixed-archive":
			seed["data/symdesk/symingest.db"] = false
			seed["data/symingest/archive"] = true
		case "contacts-overrides", "padded-overrides":
			seed["data/symdesk/symrelate.db"] = false
			padding := ""
			if id == "padded-overrides" {
				padding = " \t"
			}
			for _, pair := range [][2]string{{"SYMRELATE_DATA_HOME", "override/child/.."}, {"SYMRELATE_CONFIG_HOME", "override-config/child/.."}, {"SYMRELATE_CACHE_HOME", "override-cache/child/.."}} {
				// Keep dot components: directory overrides are raw, DB joins are cleaned.
				env[pair[0]] = padding + caseRoot + string(filepath.Separator) + filepath.FromSlash(pair[1]) + padding
			}
		case "blank-overrides":
			for _, key := range []string{"SYMRELATE_DATA_HOME", "SYMRELATE_CONFIG_HOME", "SYMRELATE_CACHE_HOME"} {
				env[key] = " \t\n"
			}
		case "home-defaults", "no-home":
			delete(env, "XDG_DATA_HOME")
			delete(env, "XDG_CONFIG_HOME")
			delete(env, "XDG_CACHE_HOME")
			if id == "no-home" {
				delete(env, "HOME")
				delete(env, "USERPROFILE")
			}
		case "xdg-without-home":
			delete(env, "HOME")
			delete(env, "USERPROFILE")
		case "relative-xdg":
			for _, key := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
				rel, err := filepath.Rel(cwd, env[key])
				if err != nil {
					return value, err
				}
				env[key] = rel
			}
		case "lexical-xdg":
			for _, key := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
				env[key] = " \t" + env[key] + string(filepath.Separator) + "missing" + string(filepath.Separator) + ".." + " \t"
			}
		case "different-home-profile":
			delete(env, "XDG_DATA_HOME")
			delete(env, "XDG_CONFIG_HOME")
			delete(env, "XDG_CACHE_HOME")
			env["USERPROFILE"] = filepath.Join(caseRoot, "profile")
		}
		for rel, directory := range seed {
			path := filepath.Join(caseRoot, filepath.FromSlash(rel))
			if directory {
				err = os.MkdirAll(path, 0o700)
			} else if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
				err = os.WriteFile(path, []byte("retained synthetic store"), 0o600)
			}
			if err != nil {
				return value, err
			}
		}
		out := storePathCase{ID: id, Root: caseRoot, Environment: maps.Clone(env)}
		out.Before, err = storePathSnapshot(caseRoot)
		if err != nil {
			return value, err
		}
		err = withEnvironment(env, func() error {
			paths := config.ContactsPaths()
			out.Contacts = [4]string{paths.ConfigDir, paths.DataDir, paths.CacheDir, paths.DBPath}
			for _, name := range []string{"symingest.db", "archive", " archive ", "./archive", "nested/../archive", `nested\file`, "..", " . ", "nested/file", "nested/../..", "../escape", "/absolute", `C:relative`, `C:\absolute`, `\absolute`, "nested/\"\n", "nested/\x00\x7f\u0085\u00a0\u200b\u2028", "nested/😀", ""} {
				resolved, resolveErr := config.IngestDataPath(name)
				item := ingestPathResult{Name: name, Value: resolved}
				if resolveErr != nil {
					item.Error = resolveErr.Error()
				}
				out.Ingest = append(out.Ingest, item)
			}
			return nil
		})
		if err != nil {
			return value, err
		}
		out.After, err = storePathSnapshot(caseRoot)
		if err != nil {
			return value, err
		}
		value.Cases = append(value.Cases, out)
		if !reflect.DeepEqual(out.Before, out.After) {
			return value, fmt.Errorf("resolver mutated %s", id)
		}
	}
	value.Complete = true
	return value, nil
}

// Manifest names, types and exact file contents; modes/times are not compared.
func storePathSnapshot(root string) (map[string]string, error) {
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result[filepath.ToSlash(rel)] = "directory"
			return nil
		}
		//nolint:gosec // path is an entry in this newly-created private capture root.
		content, err := os.ReadFile(path)
		result[filepath.ToSlash(rel)] = "file:" + hex.EncodeToString(content)
		return err
	})
	return result, err
}
