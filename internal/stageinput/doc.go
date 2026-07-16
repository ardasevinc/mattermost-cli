// Package stageinput binds attachment metadata to a verified file snapshot.
//
// Binding deliberately does not spool content. The apply layer owns a second
// secure reopen, credential scan, digest comparison, and private spool before
// upload. Therefore a path changed after Bind leaves the recorded snapshot
// untouched and must become an apply-time conflict.
//
// Darwin and Linux are supported. Linux uses O_PATH for leaf preflight. Darwin
// has no equivalent, refuses group/other-writable leaf parents, and uses
// descriptor-relative fstatat with no-follow before read-open, followed by
// mandatory descriptor identity comparison. Both reject
// non-local or unknown filesystems: context cancellation is checked between
// reads, but a blocking regular-file read itself cannot be interrupted portably.
package stageinput
