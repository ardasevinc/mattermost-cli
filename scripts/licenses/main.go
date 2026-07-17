// Command licenses enforces the reviewed license set for every module used by
// production code or tests. A new, removed, replaced, or relicensed module
// requires an explicit review and allowlist change.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type approval struct {
	license string
	marker  string
}

var approved = map[string]approval{
	"github.com/coder/websocket":               {"ISC", "Permission to use, copy, modify, and distribute this software"},
	"github.com/dustin/go-humanize":            {"MIT", "Permission is hereby granted, free of charge"},
	"github.com/google/uuid":                   {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"github.com/mattn/go-isatty":               {"MIT", "Permission is hereby granted, free of charge"},
	"github.com/ncruces/go-strftime":           {"MIT", "Permission is hereby granted, free of charge"},
	"github.com/pelletier/go-toml/v2":          {"MIT", "Permission is hereby granted, free of charge"},
	"github.com/remyoudompheng/bigfft":         {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"github.com/santhosh-tekuri/jsonschema/v6": {"Apache-2.0", "Apache License"},
	"github.com/spf13/cobra":                   {"Apache-2.0", "Apache License"},
	"github.com/spf13/pflag":                   {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"golang.org/x/net":                         {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"golang.org/x/sys":                         {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"golang.org/x/text":                        {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"modernc.org/libc":                         {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"modernc.org/mathutil":                     {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"modernc.org/memory":                       {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
	"modernc.org/sqlite":                       {"BSD-3-Clause", "Redistribution and use in source and binary forms"},
}

type module struct {
	Path    string
	Dir     string
	Main    bool
	Replace *module
}

type listedPackage struct {
	Module *module
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-test", "-json", "./...")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		fatal(err)
	}
	if err = cmd.Start(); err != nil {
		fatal(err)
	}
	modules, decodeErr := decodeModules(out)
	waitErr := cmd.Wait()
	if decodeErr != nil {
		fatal(decodeErr)
	}
	if waitErr != nil {
		fatal(waitErr)
	}
	if err = verify(modules, approved); err != nil {
		fatal(err)
	}
	fmt.Printf("license check passed: %d reviewed modules\n", len(modules))
}

func decodeModules(r io.Reader) (map[string]module, error) {
	modules := make(map[string]module)
	decoder := json.NewDecoder(r)
	for {
		var pkg listedPackage
		err := decoder.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if pkg.Module == nil || pkg.Module.Main {
			continue
		}
		if pkg.Module.Path == "" || pkg.Module.Dir == "" {
			return nil, errors.New("dependency module is missing its path or directory")
		}
		modules[pkg.Module.Path] = *pkg.Module
	}
	return modules, nil
}

func verify(modules map[string]module, allow map[string]approval) error {
	paths := make([]string, 0, len(modules))
	for path := range modules {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		mod := modules[path]
		approval, ok := allow[path]
		if !ok {
			return fmt.Errorf("unreviewed dependency module %q", path)
		}
		if mod.Replace != nil {
			return fmt.Errorf("dependency module %q uses an unreviewed replacement", path)
		}
		content, filename, err := readLicense(mod.Dir)
		if err != nil {
			return fmt.Errorf("dependency module %q: %w", path, err)
		}
		if !bytes.Contains(content, []byte(approval.marker)) {
			return fmt.Errorf("dependency module %q no longer matches reviewed %s license in %s", path, approval.license, filename)
		}
	}
	for path := range allow {
		if _, ok := modules[path]; !ok {
			return fmt.Errorf("reviewed dependency module %q is no longer used; remove its stale approval", path)
		}
	}
	return nil
}

func readLicense(dir string) ([]byte, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0)
	for _, entry := range entries {
		upper := strings.ToUpper(entry.Name())
		if !entry.Type().IsRegular() || (!strings.HasPrefix(upper, "LICENSE") && !strings.HasPrefix(upper, "COPYING")) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, "", errors.New("no root license file found")
	}
	filename := filepath.Join(dir, names[0])
	file, err := os.Open(filename)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return nil, "", err
	}
	if len(content) > 1<<20 {
		return nil, "", errors.New("license file exceeds 1 MiB")
	}
	return content, names[0], nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "license check failed:", err)
	os.Exit(1)
}
