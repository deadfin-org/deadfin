// SPDX-License-Identifier: GPL-2.0-only

package activitylog

import (
	"fmt"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// Severity values are the LogLevel names upstream writes.
const (
	SeverityInformation = "Information"
	SeverityError       = "Error"
)

// The builders below port the event consumers under
// Jellyfin.Server.Implementations/Events/Consumers. Names use the en-US
// server strings (Emby.Server.Implementations/Localization/Core/en-US.json).

func ipAddress(remoteEndPoint string) *string {
	return text(fmt.Sprintf("IP address: %s", remoteEndPoint))
}

func text(value string) *string { return &value }

// AuthenticationSucceeded ports AuthenticationSucceededLogger.
func AuthenticationSucceeded(userID dto.UUID, userName, remoteEndPoint string) Entry {
	return Entry{
		Name:          fmt.Sprintf("%s successfully authenticated", userName),
		Type:          "AuthenticationSucceeded",
		UserID:        userID,
		ShortOverview: ipAddress(remoteEndPoint),
		Severity:      SeverityInformation,
	}
}

// AuthenticationFailed ports AuthenticationFailedLogger. The entry has no user.
func AuthenticationFailed(userName, remoteEndPoint string) Entry {
	return Entry{
		Name:          fmt.Sprintf("Failed login attempt from %s", userName),
		Type:          "AuthenticationFailed",
		ShortOverview: ipAddress(remoteEndPoint),
		Severity:      SeverityError,
	}
}

// SessionStarted ports SessionStartedLogger. The caller skips a session
// without a user name.
func SessionStarted(userID dto.UUID, userName, deviceName, remoteEndPoint string) Entry {
	return Entry{
		Name:          fmt.Sprintf("%s is online from %s", userName, deviceName),
		Type:          "SessionStarted",
		UserID:        userID,
		ShortOverview: ipAddress(remoteEndPoint),
		Severity:      SeverityInformation,
	}
}

// SessionEnded ports SessionEndedLogger. The caller skips a session without a
// user name.
func SessionEnded(userID dto.UUID, userName, deviceName, remoteEndPoint string) Entry {
	return Entry{
		Name:          fmt.Sprintf("%s has disconnected from %s", userName, deviceName),
		Type:          "SessionEnded",
		UserID:        userID,
		ShortOverview: ipAddress(remoteEndPoint),
		Severity:      SeverityInformation,
	}
}

// PlaybackItem is the part of the played item that names a playback entry
// (the BaseItemDto fields that PlaybackStartLogger.GetItemName reads).
type PlaybackItem struct {
	ID         dto.UUID
	Name       string
	SeriesName string
	Artists    []string
	MediaType  string
}

func (item PlaybackItem) displayName() string {
	name := item.Name
	if item.SeriesName != "" {
		name = item.SeriesName + " - " + name
	}
	if len(item.Artists) > 0 {
		name = item.Artists[0] + " - " + name
	}
	return name
}

// PlaybackStarted ports PlaybackStartLogger. The caller skips theme media and
// a session without users.
func PlaybackStarted(userID dto.UUID, userName string, item PlaybackItem, deviceName string) Entry {
	kind := "Playback"
	switch item.MediaType {
	case "Audio":
		kind = "AudioPlayback"
	case "Video":
		kind = "VideoPlayback"
	}
	return Entry{
		Name:     fmt.Sprintf("%s is playing %s on %s", userName, item.displayName(), deviceName),
		Type:     kind,
		UserID:   userID,
		ItemID:   text(item.ID.String()),
		Severity: SeverityInformation,
	}
}

// PlaybackStopped ports PlaybackStopLogger. It reports false for a media type
// other than Audio or Video, which upstream does not log.
func PlaybackStopped(userID dto.UUID, userName string, item PlaybackItem, deviceName string) (Entry, bool) {
	var kind string
	switch item.MediaType {
	case "Audio":
		kind = "AudioPlaybackStopped"
	case "Video":
		kind = "VideoPlaybackStopped"
	default:
		return Entry{}, false
	}
	return Entry{
		Name:     fmt.Sprintf("%s has finished playing %s on %s", userName, item.displayName(), deviceName),
		Type:     kind,
		UserID:   userID,
		ItemID:   text(item.ID.String()),
		Severity: SeverityInformation,
	}, true
}

// UserCreated ports UserCreatedLogger.
func UserCreated(userID dto.UUID, userName string) Entry {
	return Entry{
		Name:     fmt.Sprintf("User %s has been created", userName),
		Type:     "UserCreated",
		UserID:   userID,
		Severity: SeverityInformation,
	}
}

// UserDeleted ports UserDeletedLogger. The entry has no user.
func UserDeleted(userName string) Entry {
	return Entry{
		Name:     fmt.Sprintf("User %s has been deleted", userName),
		Type:     "UserDeleted",
		Severity: SeverityInformation,
	}
}

// UserPasswordChanged ports UserPasswordChangedLogger.
func UserPasswordChanged(userID dto.UUID, userName string) Entry {
	return Entry{
		Name:     fmt.Sprintf("Password has been changed for user %s", userName),
		Type:     "UserPasswordChanged",
		UserID:   userID,
		Severity: SeverityInformation,
	}
}

// UserLockedOut ports UserLockedOutLogger.
func UserLockedOut(userID dto.UUID, userName string) Entry {
	return Entry{
		Name:     fmt.Sprintf("User %s has been locked out", userName),
		Type:     "UserLockedOut",
		UserID:   userID,
		Severity: SeverityError,
	}
}

// TaskFailed ports the failed branch of TaskCompletedLogger. The entry has no
// user.
func TaskFailed(taskName, errorMessage, longErrorMessage string, runningTime time.Duration) Entry {
	var lines []string
	if errorMessage != "" {
		lines = append(lines, errorMessage)
	}
	if longErrorMessage != "" {
		lines = append(lines, longErrorMessage)
	}
	return Entry{
		Name:          fmt.Sprintf("%s failed", taskName),
		Type:          "TaskFailed",
		Overview:      text(strings.Join(lines, "\n")),
		ShortOverview: text(fmt.Sprintf("Running time: %s", friendlyDuration(runningTime))),
		Severity:      SeverityError,
	}
}

// friendlyDuration ports TaskCompletedLogger.ToUserFriendlyString.
func friendlyDuration(span time.Duration) string {
	const daysInYear, daysInMonth = 365, 30
	totalSeconds := int64(span / time.Second)
	days := int(totalSeconds / 86400)
	hours := int(totalSeconds/3600) % 24
	minutes := int(totalSeconds/60) % 60
	seconds := int(totalSeconds % 60)

	var values []string
	if days >= daysInYear {
		values = append(values, valueString(days/daysInYear, "year"))
		days %= daysInYear
	}
	if days >= daysInMonth {
		values = append(values, valueString(days/daysInMonth, "month"))
		days %= daysInMonth
	}
	if days >= 1 {
		values = append(values, valueString(days, "day"))
	}
	if hours >= 1 {
		values = append(values, valueString(hours, "hour"))
	}
	if minutes >= 1 {
		values = append(values, valueString(minutes, "minute"))
	}
	if seconds >= 1 || len(values) == 0 {
		values = append(values, valueString(seconds, "second"))
	}
	var builder strings.Builder
	for index, value := range values {
		if builder.Len() > 0 {
			if index == len(values)-1 {
				builder.WriteString(" and ")
			} else {
				builder.WriteString(", ")
			}
		}
		builder.WriteString(value)
	}
	return builder.String()
}

func valueString(value int, description string) string {
	if value != 1 {
		description += "s"
	}
	// Upstream formats "{0:#,##0}"; a time.Duration holds under 293 years, so
	// no value reaches a thousands separator.
	return fmt.Sprintf("%d %s", value, description)
}
