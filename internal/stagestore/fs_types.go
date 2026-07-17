package stagestore

func linuxFilesystemTypeAllowed(kind uint64) bool {
	// Explicit local filesystems only. Unknown types fail closed.
	switch kind {
	case 0xEF53, // ext2/3/4
		0x58465342, // XFS
		0x9123683E, // btrfs
		0x794C7630, // overlayfs
		0x01021994, // tmpfs
		0x858458F6, // ramfs
		0x2FC12FC1, // ZFS
		0xF2F52010, // f2fs
		0xCA451A4E, // bcachefs
		0x00003434, // NILFS2
		0x24051905, // UBIFS
		0x3153464A, // JFS
		0x52654973, // ReiserFS
		0x00011954: // UFS
		return true
	default:
		return false
	}
}
