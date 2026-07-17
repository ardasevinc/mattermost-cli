package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyRequiresExactReviewedSetAndLicenseMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("reviewed marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	modules := map[string]module{"example.test/module": {Path: "example.test/module", Dir: dir}}
	allow := map[string]approval{"example.test/module": {license: "Test", marker: "reviewed marker"}}
	if err := verify(modules, allow); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(map[string]module, map[string]approval){
		"unreviewed": func(modules map[string]module, _ map[string]approval) {
			modules["example.test/other"] = module{Path: "example.test/other", Dir: dir}
		},
		"stale": func(_ map[string]module, allow map[string]approval) {
			allow["example.test/unused"] = approval{license: "Test", marker: "reviewed marker"}
		},
		"replaced": func(modules map[string]module, _ map[string]approval) {
			value := modules["example.test/module"]
			value.Replace = &module{Path: "example.test/replacement", Dir: dir}
			modules["example.test/module"] = value
		},
		"relicensed": func(_ map[string]module, allow map[string]approval) {
			allow["example.test/module"] = approval{license: "Test", marker: "absent"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateModules := map[string]module{"example.test/module": modules["example.test/module"]}
			candidateAllow := map[string]approval{"example.test/module": allow["example.test/module"]}
			mutate(candidateModules, candidateAllow)
			if err := verify(candidateModules, candidateAllow); err == nil {
				t.Fatal("expected verification failure")
			}
		})
	}
}

func TestDecodeModulesDeduplicatesAndRejectsIncompleteMetadata(t *testing.T) {
	input := `{"Module":{"Path":"example.test/module","Dir":"/tmp/module"}}
{"Module":{"Path":"example.test/module","Dir":"/tmp/module"}}
{"Module":{"Path":"main.test/project","Dir":"/tmp/main","Main":true}}
`
	modules, err := decodeModules(strings.NewReader(input))
	if err != nil || len(modules) != 1 || modules["example.test/module"].Dir != "/tmp/module" {
		t.Fatalf("modules=%+v err=%v", modules, err)
	}
	if _, err = decodeModules(strings.NewReader(`{"Module":{"Path":"example.test/module"}}`)); err == nil {
		t.Fatal("expected incomplete module metadata failure")
	}
}
