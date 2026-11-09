# 03 — API Surface

The parity contract = `upstream/jellyfin-openapi-stable.json` (v12.0.0; 294 paths /
364 operations / 357 schemas) **+ the shadow spec below** (served-but-hidden
routes). Operations by tag, for scale: LiveTv 38, Image 37, Library 34, SyncPlay 22,
System 19, Session 18, Plugin 17, Authentication 13, ItemLookup 11, Playlist 11,
Subtitle 10, User 10, … (21 tags have ≤ 3 ops — the long tail is real but cheap).

## Swiftfin parity set (measured, not estimated)

Grep of Swiftfin source: 118 `Paths.*` symbols = 22 parameter structs + 96
operations; `getLiveHlsStream` appears only in a comment → **95 real REST
operations**, plus SDK conveniences that never appear as `Paths.*`:
`POST /Users/AuthenticateByName`, `POST /Users/AuthenticateWithQuickConnect`,
`POST /QuickConnect/Initiate` + `GET /QuickConnect/Connect`, `GET /socket`
(WebSocket), and UDP :7359 discovery.

Buckets: A connect/login · B browse/home · C item detail · D images · E playback ·
F user data · G search/filters · H admin/misc · I LiveTv (stub). Phase = where it
must work (stubs earlier are fine if spec-shaped).

| Operation | Route | Bucket | Phase |
|---|---|---|---|
| getPublicSystemInfo | GET /System/Info/Public | A | P0 |
| getBrandingOptions | GET /Branding/Configuration | A | P1 |
| getPublicUsers | GET /Users/Public | A | P1 |
| getQuickConnectEnabled | GET /QuickConnect/Enabled | A | P1 |
| authorizeQuickConnect | POST /QuickConnect/Authorize | A | P1 |
| getCurrentUser | GET /Users/Me | A/B | P1 |
| getUserByID | GET /Users/{userId} | A/H | P1 |
| getUserImage | GET /UserImage (URL-built) | A/D | P3 |
| getSplashscreen | GET /Branding/Splashscreen (URL-built) | A/D | P3 |
| getUserViews | GET /UserViews | B | P2 |
| getItems | GET /Items | B/G | P2 |
| getLatestMedia | GET /Items/Latest | B | P2 |
| getResumeItems | GET /UserItems/Resume | B | P2 |
| getNextUp | GET /Shows/NextUp | B | P2 |
| getEpisodes | GET /Shows/{seriesId}/Episodes | B/C | P2 |
| getSeasons | GET /Shows/{seriesId}/Seasons | C | P2 |
| getItem | GET /Items/{itemId} | C | P2 |
| getSimilarItems | GET /Items/{itemId}/Similar | C | P2 |
| getSpecialFeatures | GET /Items/{itemId}/SpecialFeatures | C | P2 |
| getLocalTrailers | GET /Items/{itemId}/LocalTrailers | C | P2 |
| getAdditionalPart | GET /Videos/{itemId}/AdditionalParts | C | P2 |
| getGenres | GET /Genres | B/G | P2 |
| getPersons | GET /Persons | B/G | P2 |
| getStudios | GET /Studios | B/G | P2 |
| getQueryFiltersLegacy | GET /Items/Filters | G | P2 |
| markFavoriteItem / unmarkFavoriteItem | POST/DELETE /UserFavoriteItems/{itemId} | F | P2 |
| markPlayedItem / markUnplayedItem | POST/DELETE /UserPlayedItems/{itemId} | F | P2 |
| updateUserConfiguration | POST /Users/Configuration | F/H | P2 |
| getItemImage | GET /Items/{itemId}/Images/{type} (URL-built) | D | P3 |
| getItemImageInfos | GET /Items/{itemId}/Images | D | P3 |
| getPostedPlaybackInfo | POST /Items/{itemId}/PlaybackInfo | E | P4 |
| getBitrateTestBytes | GET /Playback/BitrateTest | E | P4 |
| getVideoStream | GET /Videos/{itemId}/stream (URL-built) | E | P5 |
| getDownload | GET /Items/{itemId}/Download | E | P5 |
| reportPlaybackStart/Progress/Stopped | POST /Sessions/Playing[/Progress|/Stopped] | E | P5 |
| stopEncodingProcess | DELETE /Videos/ActiveEncodings (**shadow spec**) | E | P6 |
| getTrickplayTileImage | GET /Videos/{itemId}/Trickplay/{width}/{index}.jpg (URL-built) | E/D | P6 |
| getSessions | GET /Sessions | H | P7 |
| play | POST /Sessions/{sessionId}/Playing | H | P7 |
| sendPlaystateCommand | POST /Sessions/{sessionId}/Playing/{command} | H | P7 |
| sendGeneralCommand | POST /Sessions/{sessionId}/Command/{command} | H | P7 |
| sendMessageCommand | POST /Sessions/{sessionId}/Message | H | P7 |
| searchRemoteSubtitles | GET /Items/{itemId}/RemoteSearch/Subtitles/{language} | H | P9* |
| downloadRemoteSubtitles | POST /Items/{itemId}/RemoteSearch/Subtitles/{subtitleId} | H | P9* |
| deleteSubtitle | DELETE /Videos/{itemId}/Subtitles/{index} | H | P9 |
| uploadSubtitle | POST /Videos/{itemId}/Subtitles | H | P9 |
| getRemoteImages / getRemoteImageProviders | GET /Items/{itemId}/RemoteImages[/Providers] | D/H | P8 |
| downloadRemoteImage | POST /Items/{itemId}/RemoteImages/Download | D/H | P8 |
| setItemImage / deleteItemImage(ByIndex) | POST/DELETE /Items/{itemId}/Images/... | D/H | P9 |
| postUserImage / deleteUserImage | POST/DELETE /UserImage | D/H | P9 |
| updateItem | POST /Items/{itemId} | H | P9 |
| refreshItem | POST /Items/{itemId}/Refresh | H | P9 |
| deleteItem | DELETE /Items/{itemId} | H | P9 |
| applySearchCriteria | POST /Items/RemoteSearch/Apply/{itemId} | H | P8 |
| get{Movie,Series,Person,BoxSet}RemoteSearchResults | POST /Items/RemoteSearch/{type} | H | P8 |
| getUsers / createUserByName / deleteUser / updateUser | /Users[...] | H | P9 |
| updateUserPassword / updateUserPolicy | POST /Users/Password, /Users/{id}/Policy | H | P9 |
| getKeys / createKey / revokeKey | /Auth/Keys[...] | H | P9 |
| getDevices / deleteDevice / updateDeviceOptions | /Devices[...] | H | P9 |
| getTasks / getTask / startTask / stopTask / updateTask | /ScheduledTasks[...] | H | P9 |
| getLogEntries | GET /System/ActivityLog/Entries | H | P9 |
| getServerLogs / getLogFile | GET /System/Logs[/Log] | H | P9 |
| restartApplication / shutdownApplication | POST /System/Restart, /System/Shutdown | H | P9 |
| getMediaFolders | GET /Library/MediaFolders | H | P9 |
| getCountries / getCultures / getParentalRatings | GET /Localization/* | H | P1 |
| getLiveTvPrograms / getRecommendedPrograms / getRecordings | GET /LiveTv/* | I | P9 (stub) |

\* Subtitle remote search/download return empty results this sprint (no
ISubtitleProvider — D6); endpoints exist and are spec-shaped so Swiftfin's UI
degrades gracefully.

**Swiftfin wire behaviors to honor** (from source, cited in the recon transcript):
- Rejects servers whose `/System/Info/Public` lacks `ServerName` or `Id`; needs
  `Version` populated. It warns when `Version` is below its SDK version
  (12.1 in Swiftfin with jellyfin-sdk-swift 3.4.0; D-128).
- `GET /QuickConnect/Enabled` body must be a **bare JSON boolean**.
- Uses `MediaSourceInfo.TranscodingUrl` **verbatim**; it must carry
  `PlaySessionId` (Swiftfin throws without it) — and MediaSource selection is
  order-dependent (eTag → openToken-contains-id → id → first).
- May send `DeviceProfile` with **empty TranscodingProfiles** (direct-play mode) —
  PlaybackInfo must not error.
- VLCKit profile relies on StreamBuilder's HLS codec allowlists
  (`StreamBuilder.cs:31-33`) so VC1 direct-streams; reproduce the allowlists
  exactly.
- Sends both `playSessionID` and `sessionID` = same value in playstate reports;
  session lookup must tolerate.
- `/Playback/BitrateTest` must serve real bytes at realistic rate (client times it).
- Opens `/socket` unconditionally with `api_key` + `deviceId` query params;
  subscribes Sessions/ActivityLog/ScheduledTasks only.

## jellyfin-web requirements beyond the spec

jellyfin-web ships **both** `@jellyfin/sdk` (generated) and `jellyfin-apiclient`
1.11.0 (legacy, hand-built URLs). The legacy path is the parity hazard.

**Legacy-built routes NOT in the spec (must exist):**
- `GET /Users/{userId}/Items` — **28+ call sites**; the entire legacy browse UI
  (movies/shows/music controllers, favorites, collection editor, screensavers).
  Accepts the same 88-param surface as `/Items`; `Ids` CSV up to 40 GUIDs (the
  web batcher chunks at 40).
- `GET /Users/{userId}/Items/Latest` (3 sites) · `GET /Users/{userId}/GroupingOptions`.
- Wizard (deprecated in spec but mandatory for first run): `GET|POST
  /Startup/Configuration`, `POST /Startup/RemoteAccess`, `GET /Startup/User` +
  `/Startup/FirstUser`, `POST /Startup/User`, `POST /Startup/Complete`.
- `GET {any}/System/Info/Public` fetched with **no auth header** by
  ConnectionRequired (reconnect probe) — must work anonymously always.

**Login → home sequence (P1/P2 checkpoint script feeds off this):**
QuickConnect Enabled + Users/Public + Branding/Configuration (LoginDisclaimer,
markdown) → AuthenticateByName (**401 invalid vs 403 unauthorized — web branches
on it**) → POST /Sessions/Capabilities/Full → **GET
/DisplayPreferences/usersettings?client=emby (blocking — home won't lay out
without it)** → GET /System/Endpoint + BitrateTest (6s later) → WS open →
GET /UserViews (**empty ⇒ web renders "NothingHere"; the T3 gate needs a
non-empty library**) → sections: /UserItems/Resume, /Shows/NextUp, /Items/Latest.

**Playback:** PlaybackInfo via SDK; direct URL hand-built
`Videos/{id}/stream.{container}?Static=true&ApiKey=...`; transcode =
`TranscodingUrl` verbatim; hls.js except Safari-native; **the Safari prefetch hack
rewrites `master.m3u8`→`live.m3u8` and GETs it for sources without RunTimeTicks —
`/Videos/{id}/live.m3u8` must exist**; `playSessionId` parsed back out of the URL;
progress via legacy REST reporters; cleanup via `DELETE /Videos/ActiveEncodings`.
Subtitle fonts: `GET /FallbackFont/Fonts`.

**WebSocket messages web handles (21):** UserDataChanged, LibraryChanged, Sessions
(dashboard + cast-to-device), ScheduledTasksInfo, RefreshProgress, Play, Playstate,
GeneralCommand (remote control), SyncPlayCommand/GroupUpdate (stubbed features),
Timer/SeriesTimer created/cancelled (LiveTv, stub-silent), PackageInstalling/
Completed/Failed/Cancelled, ServerShuttingDown/Restarting, RestartRequired.
Phase-1 minimum: accept + KeepAlive; P7 delivers the feeds and remote control.

**Plugin-tagged ops web actually calls** (10 — implement in P9 returning honest
empties): getPlugins, getPackages, getPackageInfo, installPackage,
uninstallPluginByVersion (NOT the unversioned variant), enablePlugin,
disablePlugin, getConfigurationPages, getRepositories, setRepositories. Plus
`GET /web/ConfigurationPage?name=X` (text/html — in-tree provider config pages,
P8) and `GET/POST /Plugins/{id}/Configuration` + `GET /Plugins/{id}/Image` used by
plugin-authored pages.

## Shadow spec — routes served but hidden from OpenAPI

41 `[ApiExplorerSettings(IgnoreApi = true)]` occurrences across 12 controllers.
**A server generated purely from the spec cannot play a transcoded video.**

**HLS (whole controllers hidden):**
- `GET|HEAD /Videos/{itemId}/master.m3u8` · `GET /Videos/{itemId}/main.m3u8` ·
  `GET /Videos/{itemId}/live.m3u8` ·
  `GET /Videos/{itemId}/hls1/{playlistId}/{segmentId}.{container}` and the
  `/Audio/...` equivalents (`DynamicHlsController.cs`).
- Legacy: `GET /Videos/{itemId}/hls/{playlistId}/stream.m3u8` ·
  `GET /Videos/{itemId}/hls/{playlistId}/{segmentId}.{segmentContainer}` ·
  `GET /Audio/{itemId}/hls/{segmentId}/stream.{mp3|aac}` ·
  **`DELETE /Videos/ActiveEncodings`** (both clients call it)
  (`HlsSegmentController.cs`).

**Legacy user-scoped duplicates still routed** (beyond the three web needs above):
`/Users/{userId}/Items/{itemId}`, `.../Resume`, `.../Root`,
`.../Intros|LocalTrailers|SpecialFeatures|UserData|Rating`, `/Users/{userId}/Views`,
`/Users/{userId}/FavoriteItems/{itemId}`, `/Users/{userId}/PlayedItems/{itemId}`,
`/Users/{userId}/PlayingItems/{itemId}[/Progress]`, `/PlayingItems/{itemId}[/Progress]`,
`/Users/{userId}/Images/{imageType}[/{index}]`,
`/Users/{userId}/Authenticate|Password|Configuration`, `POST /Users/{userId}`,
`/Users/{userId}/Suggestions`, `/Artists/InstantMix`, `/Recordings/Series`,
`/Recordings/Groups`, root `GET /MusicGenres`.
Policy: implement the ones clients use (marked above) in their phase; the rest are
thin aliases onto the same handlers — cheap; do them in the same WP as their
modern twin and record them in the ledger as distinct rows.

**Deliverable discipline:** `ledger/shadow-spec.yaml` enumerates every hidden route
with its upstream controller/line and which client uses it; difftool corpora must
cover each at least once (coverage check in P9's gate).

## Wire-level trap list (binding/rendering layer requirements)

1. Auth token forms, precedence order (`AuthorizationContext.cs:90-110`):
   `Authorization: MediaBrowser ...` / `X-Emby-Authorization` (parsed dictionary
   form) → `X-Emby-Token` (legacy-gated) → `X-MediaBrowser-Token` (legacy-gated) →
   `?ApiKey=` (**always**) → `?api_key=` (legacy-gated). The spec documents none
   of this.
2. JSON casing negotiation: 3 media types on 362 ops; `Accept: application/json;
   profile="CamelCase"` re-cases struct keys only, never map keys.
3. Query params: CSV **and** repeated forms both accepted; enum values
   case-insensitive; `/Items` has 88 params.
4. Bodies: `application/json` + `text/json` + `application/*+json`; raw `image/*`
   uploads (base64 body for user image); **no multipart anywhere**.
5. `Range` requests on `/Videos/{id}/stream` are undocumented and mandatory.
6. HLS `params` query value = legacy pipe-delimited positional encoding decoded by
   `StreamingHelpers.ParseParams` — must be ported.
7. `GET /QuickConnect/Enabled` → bare `true`/`false` JSON body.
8. Login errors: 401 (invalid credentials) vs 403 (valid but not allowed) —
   clients branch.
9. Keep the misspelled `/LiveTv/Tuners/Discvover` sibling route (both in spec and
   Swift SDK).
10. Path-segment extensions (`stream.{container}`, `{segmentId}.{container}`,
    `/Branding/Css.css`, subtitle `Stream.{routeFormat}`) — router must support
    mid-segment params (chi does; see 01).
11. GUIDs out = 32-hex lowercase; in = any format. Times = UTC ISO-8601, 7-digit
    fraction.
12. Case-insensitive route matching (ASP.NET default) — clients mix casings.
13. `text/html` error variants declared on every op — error bodies are
    ProblemDetails JSON by default; match difftool corpus.
14. WebSocket is absent from spec paths but modeled in schemas
    (discriminated unions on `MessageType`); `/socket` is hand-written.
15. TranscodingUrl (not in spec) is consumed verbatim by both clients and must
    carry PlaySessionId.

## Stub / deferred policy

- **LiveTv (38 ops):** `GET /LiveTv/Info` reports `IsEnabled=false`, zero tuners;
  list endpoints return empty QueryResults; mutating endpoints 404 like upstream-
  with-no-tuners where observable, else empty-success. Timer WS events never fire.
- **SyncPlay (22 ops):** `/SyncPlay/List` returns `[]`; capability surfaces report
  disabled (`SyncPlayAccess` policy denies); web hides the UI.
- **Plugin tag (17 ops):** read endpoints return `[]`/empty (dashboard renders an
  empty plugins page, no throw); install/uninstall return a spec-shaped error
  ("plugin support not available"). `/web/ConfigurationPages` lists the in-tree
  provider pages only (P8).
- **Subtitle remote search (D6 cost):** empty result sets, correct shapes.
- Everything stubbed is a `stubbed`/`deferred` row in the ledger — never a 404
  surprise. P9's exit gate replays full client captures and requires zero 404s.
