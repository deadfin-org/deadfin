// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

// DeviceService is the device side of the identity service that the Devices
// controller reads and writes. *user.Service satisfies it.
type DeviceService interface {
	Devices(ctx context.Context, userID dto.UUID, persistentIdentifier func(deviceID string) bool) ([]store.DeviceRecord, error)
	Device(ctx context.Context, deviceID string) (store.DeviceRecord, error)
	DeviceTokens(ctx context.Context, deviceID string) ([]store.DeviceRecord, error)
	DeviceOptions(ctx context.Context, deviceID string) (store.DeviceOptions, error)
	UpdateDeviceOptions(ctx context.Context, deviceID string, customName *string) error
	Logout(ctx context.Context, token string) error
}

// DeviceSessions is the session registry that the Devices controller reads
// for capabilities and ends sessions in. *session.Manager satisfies it.
type DeviceSessions interface {
	Sessions(query session.Query) []session.Snapshot
	EndDeviceSessions(deviceID string) []string
}

// RegisterDevices registers the Devices controller, ported from
// upstream/jellyfin/Jellyfin.Api/Controllers/DevicesController.cs. The
// controller requires elevation (Policies.RequiresElevation, :25).
func RegisterDevices(rt *router.Router, devices DeviceService, sessions DeviceSessions, sockets SessionSockets) {
	h := devicesHandler{devices: devices, sessions: sessions, sockets: sockets}
	policy := string(auth.PolicyRequiresElevation)
	rt.Handle(http.MethodGet, "/Devices", router.Route{OperationID: "GetDevices", Policy: policy}, h.available(h.list))
	rt.Handle(http.MethodDelete, "/Devices", router.Route{OperationID: "DeleteDevice", Policy: policy}, h.available(h.delete))
	rt.Handle(http.MethodGet, "/Devices/Info", router.Route{OperationID: "GetDeviceInfo", Policy: policy}, h.available(h.info))
	rt.Handle(http.MethodGet, "/Devices/Options", router.Route{OperationID: "GetDeviceOptions", Policy: policy}, h.available(h.options))
	rt.Handle(http.MethodPost, "/Devices/Options", router.Route{OperationID: "UpdateDeviceOptions", Policy: policy}, h.available(h.updateOptions))
}

// available answers 503 when the composition root has no identity service,
// which keeps the pure route ledger path valid.
func (h devicesHandler) available(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.devices == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		next(w, r)
	}
}

type devicesHandler struct {
	devices  DeviceService
	sessions DeviceSessions
	sockets  SessionSockets
}

// deviceInfoDto is MediaBrowser.Model/Dto/DeviceInfoDto.cs. DeviceManager
// never sets AccessToken, so it is always omitted. IconUrl comes from the
// capabilities that the device reported (divergence D-96).
type deviceInfoDto struct {
	Name             string                 `json:"Name"`
	CustomName       *string                `json:"CustomName,omitzero"`
	ID               string                 `json:"Id"`
	LastUserName     string                 `json:"LastUserName"`
	AppName          string                 `json:"AppName"`
	AppVersion       string                 `json:"AppVersion"`
	LastUserID       dto.UUID               `json:"LastUserId"`
	DateLastActivity dto.Time               `json:"DateLastActivity"`
	Capabilities     dto.ClientCapabilities `json:"Capabilities"`
	IconURL          *string                `json:"IconUrl,omitzero"`
}

// deviceOptionsDto is Jellyfin.Data/Dtos/DeviceOptionsDto.cs. Id is the
// upstream row ID; Deadfin keys the options by device ID and writes 0
// (divergence D-97).
type deviceOptionsDto struct {
	ID         int     `json:"Id"`
	DeviceID   string  `json:"DeviceId"`
	CustomName *string `json:"CustomName,omitzero"`
}

type devicesQuery struct {
	UserID *dto.UUID `query:"userId"`
}

type deviceIDQuery struct {
	ID *string `query:"id"`
}

type deviceIDsQuery struct {
	ID []string `query:"id"`
}

// list ports GetDevices (DevicesController.cs:51-56). RequestHelpers.GetUserId
// falls back to the caller when userId is absent, so an administrator sees
// every device and an API key, which has no user, sees every device.
func (h devicesHandler) list(w http.ResponseWriter, r *http.Request) {
	var q devicesQuery
	if err := bind.QueryRequest(r, &q); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	userID := dto.NilUUID
	if q.UserID != nil && *q.UserID != dto.NilUUID {
		userID = *q.UserID
	} else if identity, ok := middleware.IdentityFromContext(r.Context()); ok {
		userID = identity.UserID
	}
	records, err := h.devices.Devices(r.Context(), userID, h.persistentIdentifier)
	if err != nil {
		writeDeviceError(w, err)
		return
	}
	items := make([]deviceInfoDto, 0, len(records))
	for _, record := range records {
		items = append(items, h.deviceInfo(record))
	}
	// QueryResult(IReadOnlyList) sets TotalRecordCount to the item count and
	// StartIndex to 0 (QueryResult.cs).
	_ = render.JSON(w, r, http.StatusOK, dto.QueryResult[deviceInfoDto]{Items: items, TotalRecordCount: len(items)})
}

// info ports GetDeviceInfo (DevicesController.cs:66-77).
func (h devicesHandler) info(w http.ResponseWriter, r *http.Request) {
	id, ok := requiredDeviceID(w, r)
	if !ok {
		return
	}
	record, err := h.devices.Device(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeNotFoundProblem(w)
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, h.deviceInfo(record))
}

// options ports GetDeviceOptions (DevicesController.cs:87-98). A device
// without saved options is 404, also when it has tokens.
func (h devicesHandler) options(w http.ResponseWriter, r *http.Request) {
	id, ok := requiredDeviceID(w, r)
	if !ok {
		return
	}
	options, err := h.devices.DeviceOptions(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeNotFoundProblem(w)
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, deviceOptionsDto{DeviceID: options.DeviceID, CustomName: options.CustomName})
}

// updateOptions ports UpdateDeviceOptions (DevicesController.cs:108-115). It
// saves CustomName for any device ID, also one without tokens.
func (h devicesHandler) updateOptions(w http.ResponseWriter, r *http.Request) {
	id, ok := requiredDeviceID(w, r)
	if !ok {
		return
	}
	body, ok := configurationBody(w, r)
	if !ok {
		return
	}
	var posted struct {
		CustomName *string
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) || json.Unmarshal(body, &posted) != nil {
		writeValidationError(w, "deviceOptions")
		return
	}
	if err := h.devices.UpdateDeviceOptions(r.Context(), id, posted.CustomName); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// delete ports DeleteDevice (DevicesController.cs:125-145). An unknown device
// fails the whole request with 400 before any change. Each token of each
// device is logged out: SessionManager.Logout(Device) deletes the token and
// ends the device's sessions (SessionManager.cs:1759-1785).
func (h devicesHandler) delete(w http.ResponseWriter, r *http.Request) {
	var q deviceIDsQuery
	if err := bind.QueryRequest(r, &q); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	tokens := make([][]store.DeviceRecord, 0, len(q.ID))
	for _, id := range q.ID {
		records, err := h.devices.DeviceTokens(r.Context(), id)
		if err != nil {
			internalError(w)
			return
		}
		if len(records) == 0 {
			writeBadRequestProblem(w)
			return
		}
		tokens = append(tokens, records)
	}
	for _, records := range tokens {
		for _, record := range records {
			if err := h.devices.Logout(r.Context(), record.AccessToken); err != nil {
				internalError(w)
				return
			}
			if h.sessions == nil {
				continue
			}
			for _, sessionID := range h.sessions.EndDeviceSessions(record.DeviceID) {
				if h.sockets != nil {
					_ = h.sockets.CloseSession(r.Context(), sessionID)
				}
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// deviceInfo ports DeviceManager.ToDeviceInfo and ToDeviceInfoDto
// (DeviceManager.cs:251-297). ToDeviceInfo does not copy the capabilities, so
// Capabilities is always the ClientCapabilities default; only IconUrl comes
// from the reported capabilities.
func (h devicesHandler) deviceInfo(record store.DeviceRecord) deviceInfoDto {
	return deviceInfoDto{
		Name:             record.DeviceName,
		CustomName:       record.CustomName,
		ID:               record.DeviceID,
		LastUserName:     record.UserName,
		AppName:          record.AppName,
		AppVersion:       record.AppVersion,
		LastUserID:       record.UserID,
		DateLastActivity: dto.Time{Time: record.DateLastActivity},
		Capabilities: dto.ClientCapabilities{
			PlayableMediaTypes:           dto.CommaDelimitedStrings{},
			SupportedCommands:            dto.CommaDelimitedStrings{},
			SupportsPersistentIdentifier: true,
		},
	}
}

// persistentIdentifier reports the SupportsPersistentIdentifier capability of
// a device. DeviceManager.GetCapabilities defaults to true when the device
// has reported no capabilities (ClientCapabilities.cs:12-18). Deadfin knows
// the capabilities of a device while it has a live session (D-96).
func (h devicesHandler) persistentIdentifier(deviceID string) bool {
	if h.sessions == nil {
		return true
	}
	snapshots := h.sessions.Sessions(session.Query{DeviceID: deviceID})
	if len(snapshots) == 0 {
		return true
	}
	return snapshots[0].Capabilities.SupportsPersistentIdentifier
}

// requiredDeviceID binds the [FromQuery, Required] string id. A missing or
// empty value fails model validation with 400.
func requiredDeviceID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var q deviceIDQuery
	if err := bind.QueryRequest(r, &q); err != nil || q.ID == nil || *q.ID == "" {
		writeValidationError(w, "id")
		return "", false
	}
	return *q.ID, true
}

// writeDeviceError maps an unknown userId, which DeviceManager reports with
// ResourceNotFoundException, to the ExceptionMiddleware 404
// (ExceptionMiddleware.cs:123-136). Any other failure is 500.
func writeDeviceError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "Error processing request.")
}

// writeBadRequestProblem writes the body of ControllerBase.BadRequest() under
// [ApiController]: the client-error ProblemDetails.
func writeBadRequestProblem(w http.ResponseWriter) {
	body := []byte(`{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"Bad Request","status":400,"traceId":"` +
		generateTraceID() + `"}`)
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(body)
}
