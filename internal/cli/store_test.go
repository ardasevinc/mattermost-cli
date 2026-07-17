//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

func TestStoreDoctorAbsentIsReadOnlyAndSchemaValid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "hostile\x1bstate"))
	t.Setenv("MM_URL", "not-a-url")
	t.Setenv("MM_TOKEN", "unused-token")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store", "doctor"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.ContainsRune(stdout.String(), '\x1b') || !strings.Contains(stdout.String(), `\\u001b`) {
		t.Fatalf("path was not safely presented: %q", stdout.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/store-doctor", bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("schema: %v\n%s", err, stdout.String())
	}
	for _, fact := range []string{`"filesystemSafe":null`, `"applicationId":null`, `"integrity":null`, `"applied":null`, `"latest":11`, `"valid":null`, `"journalMode":null`} {
		if !strings.Contains(stdout.String(), fact) {
			t.Fatalf("absent report omitted %s: %s", fact, stdout.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, "hostile\x1bstate")); !os.IsNotExist(err) {
		t.Fatalf("inspection created state: %v", err)
	}
}

func TestBareStoreRejectsJSONWithMachineError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/error", bytes.NewReader(stderr.Bytes())); err != nil {
		t.Fatalf("error schema: %v\n%s", err, stderr.String())
	}
}

func TestStoreDoctorExistingDoesNotMutate(t *testing.T) {
	home := t.TempDir()
	stateRoot := filepath.Join(home, "state")
	paths, err := stagestore.ResolvePaths(home, func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := stagestore.Open(context.Background(), paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store", "doctor"}, strings.NewReader(""), &stdout, &stderr)
	after, err := os.Stat(paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"exists":true`) || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("exit=%d stdout=%q stderr=%q before=%v after=%v", code, stdout.String(), stderr.String(), before, after)
	}
}

func TestStoreMigrationsIsOfflineAndSchemaValid(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MM_URL", "not-a-url")
	t.Setenv("MM_TOKEN", "unused-token")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store", "migrations"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/store-migrations", bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("schema: %v\n%s", err, stdout.String())
	}
	want := "{\"schema\":\"mm/v2/store-migrations\",\"latest\":11,\"migrations\":[{\"version\":1,\"name\":\"core-stage-state\",\"checksum\":\"e69a3e2524903dbdb4ef5a9691ce8674fac8fe284d09e95bc5983ec9e9fef92f\"},{\"version\":2,\"name\":\"immutable-local-request-receipts\",\"checksum\":\"ac1c291e7201786935cce68dd459f61150048c505e4f29ad813304c309009b8c\"},{\"version\":3,\"name\":\"caller-intent-stage-create-replay\",\"checksum\":\"237315b734e034951a7394ef708273af5f91ea6a44abe9927537571fbcee78d5\"},{\"version\":4,\"name\":\"caller-intent-stage-revise-replay\",\"checksum\":\"c4076ad9dba0494142f0a9cddd94e1ddcfb9c6384729f5a5f54098c8eb24f9e4\"},{\"version\":5,\"name\":\"revision-plan-follows-composition\",\"checksum\":\"fd14281b59cc1887375e125b6b782a1e4b1eff200b9e78286ec067d0286851e0\"},{\"version\":6,\"name\":\"durable-apply-journal\",\"checksum\":\"4a87ce0c406f1145b6f03a18d931681afa40dbe94ea69a89f655aa15de6bbd56\"},{\"version\":7,\"name\":\"status-confirmed-delete-results\",\"checksum\":\"bf980f24ff24fdf9d6a3a28b9ded97b51336fa1c8ef290d1bdcf91f1b593c5ce\"},{\"version\":8,\"name\":\"already-satisfied-edit-apply\",\"checksum\":\"cff30dd4fcc987876e092e2de1f5afd21ebeadbcb792fd550a56eb4c221edd10\"},{\"version\":9,\"name\":\"attachment-identity-binding\",\"checksum\":\"d0782e01014d010e5978b39bf6f04ef26508bec9318c7ace3be8bfbbcab4999d\"},{\"version\":10,\"name\":\"validated-upload-reuse\",\"checksum\":\"75088d75d41eab9d7d1b1c7b1fa6fc4ce604849321b88feeb110d7ada3447947\"},{\"version\":11,\"name\":\"retention-lifecycle-audit\",\"checksum\":\"f57092f61a66e5b10e6c748fcb85f2dc6ca2fe965d1c4ad819e399e56602f941\"}]}\n"
	if stdout.String() != want {
		t.Fatalf("stdout does not match golden migration contract: %q", stdout.String())
	}
}

func TestStoreDoctorUnsafeStateIsMachineError(t *testing.T) {
	home := t.TempDir()
	stateRoot := filepath.Join(home, "state")
	db := filepath.Join(stateRoot, "mattermost-cli", stagestore.DatabaseFilename)
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store", "doctor"}, strings.NewReader(""), &stdout, &stderr)
	if code != 3 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"read_failed"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStoreDoctorUnhealthyIntegrityReturnsStateConflictExit(t *testing.T) {
	home := t.TempDir()
	stateRoot := filepath.Join(home, "state")
	paths, err := stagestore.ResolvePaths(home, func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := stagestore.Open(context.Background(), paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=OFF; INSERT INTO stage_attachments(stage_id,revision,ordinal,supplied_path,canonical_path,remote_filename,byte_length,content_digest) VALUES('missing',1,0,'a','a','a',0,zeroblob(32))`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store", "doctor"}, strings.NewReader(""), &stdout, &stderr)
	if code != 6 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"foreignKeyIssues":1`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStoreNoRedactDisablesOnlyHeuristics(t *testing.T) {
	home := t.TempDir()
	const credential = "active-mm-credential-123456"
	const heuristic = "AKIAIOSFODNN7EXAMPLE"
	t.Setenv("HOME", home)
	t.Setenv("MM_TOKEN", credential)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, heuristic, credential))
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "--no-redact", "store", "doctor"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), heuristic) || strings.Contains(stdout.String(), credential) || !strings.Contains(stdout.String(), "REDACTED") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStoreDoctorResolvesRedactionFromEnvironmentAndFileOffline(t *testing.T) {
	const heuristic = "AKIAIOSFODNN7EXAMPLE"
	for _, test := range []struct {
		name string
		env  bool
	}{{"environment", true}, {"file", false}} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, heuristic))
			if test.env {
				t.Setenv("MM_REDACT", "false")
			} else {
				previous, present := os.LookupEnv("MM_REDACT")
				if err := os.Unsetenv("MM_REDACT"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if present {
						_ = os.Setenv("MM_REDACT", previous)
					} else {
						_ = os.Unsetenv("MM_REDACT")
					}
				})
				writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), "redact = false\ntoken = \"core-stage-state\"\n", 0o600)
			}
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), []string{"--json", "store", "doctor"}, strings.NewReader(""), &stdout, &stderr)
			if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), heuristic) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestStoreMigrationsActiveCredentialCollisionFailsBeforeOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "--token", "core-stage-state", "store", "migrations"}, strings.NewReader(""), &stdout, &stderr)
	if code != 3 || stdout.Len() != 0 || strings.Contains(stderr.String(), "core-stage-state") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/error", bytes.NewReader(stderr.Bytes())); err != nil {
		t.Fatalf("error schema: %v\n%s", err, stderr.String())
	}
}

func TestStoreMigrationsTreatsReadFileTokenAsActiveCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MM_TOKEN", "")
	writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), "token = \"core-stage-state\"\n", 0o600)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store", "migrations"}, strings.NewReader(""), &stdout, &stderr)
	if code != 3 || stdout.Len() != 0 || strings.Contains(stderr.String(), "core-stage-state") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStoreDoctorMasksConfiguredTokenReadForPreference(t *testing.T) {
	home := t.TempDir()
	const token = "configured-mm-token-123456"
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, token))
	t.Setenv("MM_TOKEN", "")
	writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), "redact = false\ntoken = \""+token+"\"\n", 0o600)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store", "doctor"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || strings.Contains(stdout.String(), token) || !strings.Contains(stdout.String(), "REDACTED") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStoreArgValidationMasksConfiguredTokenBeforePreRun(t *testing.T) {
	home := t.TempDir()
	const token = "configured-mm-token-arg-123456"
	t.Setenv("HOME", home)
	t.Setenv("MM_TOKEN", "")
	writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), "token = \""+token+"\"\n", 0o600)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "store", token}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || strings.Contains(stderr.String(), token) || !strings.Contains(stderr.String(), "REDACTED") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/error", bytes.NewReader(stderr.Bytes())); err != nil {
		t.Fatalf("error schema: %v\n%s", err, stderr.String())
	}
}

func TestStoreMigrationRuntimeRejectsInvalidOrderBeforeWrite(t *testing.T) {
	var stdout, stderr bytes.Buffer
	state := &rootState{streams: streams{out: &stdout, err: &stderr}}
	err := writeStoreMigrations(state, []stagestore.MigrationInfo{{Version: 2, Name: "later", Checksum: strings.Repeat("a", 64)}})
	if err == nil || stdout.Len() != 0 {
		t.Fatalf("err=%v stdout=%q", err, stdout.String())
	}
}

func TestStoreMigrationRuntimeRejectsUnsafeMetadata(t *testing.T) {
	for _, migration := range []stagestore.MigrationInfo{{Version: 1, Name: "bad name", Checksum: strings.Repeat("a", 64)}, {Version: 1, Name: "safe-name", Checksum: strings.Repeat("G", 64)}} {
		var stdout bytes.Buffer
		state := &rootState{streams: streams{out: &stdout}}
		if err := writeStoreMigrations(state, []stagestore.MigrationInfo{migration}); err == nil || stdout.Len() != 0 {
			t.Fatalf("migration=%+v err=%v stdout=%q", migration, err, stdout.String())
		}
	}
}

func TestStoreHumanOutputStatesBounds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "")
	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"store", "doctor"}, strings.NewReader(""), &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "exists: false") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	stdout.Reset()
	if code := Execute(context.Background(), []string{"store", "migrations"}, strings.NewReader(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "latest: 11\n1 core-stage-state ") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
