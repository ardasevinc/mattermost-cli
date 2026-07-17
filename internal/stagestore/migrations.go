package stagestore

// MigrationInfo is immutable metadata for one migration compiled into mm.
type MigrationInfo struct {
	Version  int
	Name     string
	Checksum string
}

// Migrations returns the ordered migration set without opening local state.
func Migrations() []MigrationInfo {
	result := make([]MigrationInfo, len(migrations))
	for i, item := range migrations {
		result[i] = MigrationInfo{Version: item.version, Name: item.name, Checksum: item.checksum()}
	}
	return result
}
