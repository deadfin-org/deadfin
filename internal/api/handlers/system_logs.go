// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/library"
)

// RegisterSystemLogs registers GET /System/Logs and GET /System/Logs/Log,
// ported from upstream/jellyfin/Jellyfin.Api/Controllers/SystemController.cs:
// 146-176 and 206-225. Both require elevation. logDirectory is
// IServerApplicationPaths.LogDirectoryPath. Deadfin writes its own log to
// stderr, so the directory holds the ffmpeg job logs and the uploaded client
// logs (divergence D-98).
func RegisterSystemLogs(rt *router.Router, logDirectory string) {
	h := systemLogsHandler{directory: logDirectory}
	policy := string(auth.PolicyRequiresElevation)
	rt.Handle(http.MethodGet, "/System/Logs", router.Route{OperationID: "GetServerLogs", Policy: policy}, h.list)
	rt.Handle(http.MethodGet, "/System/Logs/Log", router.Route{OperationID: "GetLogFile", Policy: policy}, h.file)
}

type systemLogsHandler struct {
	directory string
}

// logFile is MediaBrowser.Model/System/LogFile.cs.
type logFile struct {
	DateCreated  dto.Time `json:"DateCreated"`
	DateModified dto.Time `json:"DateModified"`
	Size         int64    `json:"Size"`
	Name         string   `json:"Name"`
}

type logFileQuery struct {
	Name *string `query:"name"`
}

// list ports GetServerLogs. It lists the regular files directly in the log
// directory whose extension is .txt or .log, compared without case
// (ManagedFileSystem.GetFiles, ManagedFileSystem.cs:587-622). A directory
// that cannot be read gives an empty list. The order is newest modification
// first, then newest creation, then name.
func (h systemLogsHandler) list(w http.ResponseWriter, r *http.Request) {
	files := h.files()
	result := make([]logFile, 0, len(files))
	for _, file := range files {
		if ext := filepath.Ext(file.name); !strings.EqualFold(ext, ".txt") && !strings.EqualFold(ext, ".log") {
			continue
		}
		result = append(result, logFile{
			DateCreated:  dto.Time{Time: file.created},
			DateModified: dto.Time{Time: file.info.ModTime().UTC()},
			Size:         file.info.Size(),
			Name:         file.name,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if !left.DateModified.Equal(right.DateModified.Time) {
			return left.DateModified.After(right.DateModified.Time)
		}
		if !left.DateCreated.Equal(right.DateCreated.Time) {
			return left.DateCreated.After(right.DateCreated.Time)
		}
		return left.Name < right.Name
	})
	_ = render.JSON(w, r, http.StatusOK, result)
}

// file ports GetLogFile. The name selects a file directly in the log
// directory, compared without case, so a name with a path separator matches
// nothing. The body is the file as text/plain; charset=utf-8.
func (h systemLogsHandler) file(w http.ResponseWriter, r *http.Request) {
	var q logFileQuery
	if err := bind.QueryRequest(r, &q); err != nil || q.Name == nil || *q.Name == "" {
		writeValidationError(w, "name")
		return
	}
	for _, file := range h.files() {
		if !strings.EqualFold(file.name, *q.Name) {
			continue
		}
		content, err := os.Open(filepath.Join(h.directory, file.name))
		if err != nil {
			internalError(w)
			return
		}
		defer content.Close()
		// File(stream, contentType) is a FileStreamResult without range
		// processing.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, content)
		return
	}
	// NotFound("Log file not found.") writes the string through the JSON
	// output formatter.
	_ = render.JSON(w, r, http.StatusNotFound, "Log file not found.")
}

type logDirectoryFile struct {
	name    string
	info    os.FileInfo
	created time.Time
}

// files returns the regular files directly in the log directory, following
// symbolic links. Entries that cannot be read are skipped
// (EnumerationOptions.IgnoreInaccessible, ManagedFileSystem.cs:711-720).
func (h systemLogsHandler) files() []logDirectoryFile {
	if h.directory == "" {
		return nil
	}
	entries, err := os.ReadDir(h.directory)
	if err != nil {
		return nil
	}
	files := make([]logDirectoryFile, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(h.directory, entry.Name())
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		created := library.FileCreatedAt(path, info)
		if created.IsZero() {
			created = info.ModTime()
		}
		files = append(files, logDirectoryFile{name: entry.Name(), info: info, created: created.UTC()})
	}
	return files
}
