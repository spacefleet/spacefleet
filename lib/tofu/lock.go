package tofu

import (
	"strings"
)

// LockInfo is the state lock OpenTofu reports when a command cannot acquire
// one — the "Lock Info:" block of its "Error acquiring the state lock"
// message. It identifies the lock a force-unlock releases (ID) and who left
// it (Who, Created, Operation), so an operator can decide whether the run
// that holds it is really gone before releasing it. Parsed from a failed
// step's captured logs; every field is what OpenTofu printed, verbatim.
type LockInfo struct {
	ID        string `json:"id"`
	Path      string `json:"path,omitempty"`
	Operation string `json:"operation,omitempty"`
	Who       string `json:"who,omitempty"`
	Version   string `json:"version,omitempty"`
	Created   string `json:"created,omitempty"`
	Info      string `json:"info,omitempty"`
}

// lockErrorMarker is the message OpenTofu prints (in every backend) when the
// state lock is already held; the "Lock Info:" block follows it.
const lockErrorMarker = "Error acquiring the state lock"

// ParseLockInfo extracts the lock a step failed to acquire from its captured
// logs, or nil when the logs carry no lock error (or a lock error without an
// id — there is then nothing a force-unlock could target). The block looks
// like:
//
//	│ Error: Error acquiring the state lock
//	│
//	│ Error message: ConditionalCheckFailedException: The conditional request failed
//	│ Lock Info:
//	│   ID:        6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f
//	│   Path:      acme-state/prod/terraform.tfstate
//	│   Operation: OperationTypeApply
//	│   Who:       kyle@laptop
//	│   Version:   1.8.5
//	│   Created:   2026-09-01 12:34:56.789012 +0000 UTC
//	│   Info:
//
// The box-drawing gutter (`│`, present even with -no-color) is stripped; the
// block ends at the first blank line or the first line that is not a
// `Key: value` pair. The first lock error in the logs wins.
func ParseLockInfo(logs string) *LockInfo {
	i := strings.Index(logs, lockErrorMarker)
	if i < 0 {
		return nil
	}
	var info LockInfo
	fields := map[string]*string{
		"ID": &info.ID, "Path": &info.Path, "Operation": &info.Operation,
		"Who": &info.Who, "Version": &info.Version, "Created": &info.Created, "Info": &info.Info,
	}
	inBlock := false
	for _, raw := range strings.Split(logs[i:], "\n") {
		line := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), "│╷╵"))
		if !inBlock {
			if line == "Lock Info:" {
				inBlock = true
			}
			continue
		}
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			break
		}
		dst, known := fields[strings.TrimSpace(key)]
		if !known {
			// A line that is not one of the lock's fields ends the block.
			break
		}
		*dst = strings.TrimSpace(value)
	}
	if info.ID == "" {
		return nil
	}
	return &info
}
