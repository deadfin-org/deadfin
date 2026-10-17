// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// maxClientLogDocumentBytes ports ClientLogController.MaxDocumentSize.
const maxClientLogDocumentBytes = 1_000_000

// clientLogTooLarge is the 413 body of ClientLogController.LogFile:
// $"Payload must be less than {MaxDocumentSize:N0} bytes".
const clientLogTooLarge = "Payload must be less than 1,000,000 bytes"

// ClientLogConfig supplies the dependencies of POST /ClientLog/Document.
type ClientLogConfig struct {
	// Enabled reads ServerConfiguration.AllowClientLogUpload per request.
	Enabled func() bool
	// LogDirectory is the server log directory (IServerApplicationPaths.LogDirectoryPath).
	LogDirectory string
	Clock        clock.Clock
	// Random supplies the 16 bytes of the Guid in the file name.
	Random io.Reader
}

// RegisterClientLog registers POST /ClientLog/Document. It ports
// ClientLogController.LogFile and ClientEventLogger.WriteDocumentAsync.
func RegisterClientLog(rt *router.Router, cfg ClientLogConfig) {
	h := clientLogHandler{cfg: cfg}
	rt.Handle(http.MethodPost, "/ClientLog/Document", router.Route{
		OperationID: "LogFile",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, h.document)
}

type clientLogHandler struct{ cfg ClientLogConfig }

func (h clientLogHandler) document(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Enabled == nil || !h.cfg.Enabled() {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, err := bind.LimitedBody(w, r, maxClientLogDocumentBytes)
	if err != nil {
		_ = render.JSON(w, r, http.StatusRequestEntityTooLarge, clientLogTooLarge)
		return
	}

	identity, _ := middleware.IdentityFromContext(r.Context())
	clientName := identity.Client
	if clientName == "" {
		clientName = "unknown-client"
	}
	clientVersion := identity.Version
	if identity.APIKey {
		clientVersion = "apikey"
	} else if clientVersion == "" {
		clientVersion = "unknown-version"
	}

	fileName, err := h.write(clientName, clientVersion, body)
	if errors.Is(err, bind.ErrBodyTooLarge) {
		_ = render.JSON(w, r, http.StatusRequestEntityTooLarge, clientLogTooLarge)
		return
	}
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, dto.ClientLogDocumentResponseDto{FileName: fileName})
}

// write ports ClientEventLogger.WriteDocumentAsync. The file name is
// upload_{client}_{version}_{yyyyMMddHHmmss UTC}_{Guid:N}.log in the log
// directory. A body that fails mid-stream leaves no partial file.
func (h clientLogHandler) write(clientName, clientVersion string, contents io.Reader) (string, error) {
	var guid [16]byte
	if _, err := io.ReadFull(h.cfg.Random, guid[:]); err != nil {
		return "", fmt.Errorf("client log guid: %w", err)
	}
	fileName := fmt.Sprintf("upload_%s_%s_%s_%s.log",
		safeLeafFileName(clientName, "unknown-client"),
		safeLeafFileName(clientVersion, "unknown-version"),
		h.cfg.Clock.Now().UTC().Format("20060102150405"),
		hex.EncodeToString(guid[:]))
	if err := os.MkdirAll(h.cfg.LogDirectory, 0o755); err != nil {
		return "", fmt.Errorf("client log directory: %w", err)
	}
	path := filepath.Join(h.cfg.LogDirectory, fileName)
	if filepath.Dir(path) != filepath.Clean(h.cfg.LogDirectory) {
		return "", errors.New("client log path resolved outside the log directory")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create client log: %w", err)
	}
	_, copyErr := io.Copy(file, contents)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", errors.Join(copyErr, closeErr)
	}
	return fileName, nil
}

// safeLeafFileName ports PathHelper.GetSafeLeafFileName on Linux: the text
// after the last '/', or fallback when that is empty, "." or "..".
func safeLeafFileName(name, fallback string) string {
	leaf := name[strings.LastIndexByte(name, '/')+1:]
	if leaf == "" || leaf == "." || leaf == ".." {
		return fallback
	}
	return leaf
}
