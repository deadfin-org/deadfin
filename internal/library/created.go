// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"os"
	"time"
)

// FileCreatedAt returns the creation time that .NET FileSystemInfo.
// CreationTimeUtc reports for path. It is zero when the platform has no
// creation fact.
func FileCreatedAt(path string, info os.FileInfo) time.Time {
	return entryCreatedAt(path, info)
}
