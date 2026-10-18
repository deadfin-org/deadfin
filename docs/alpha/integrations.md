# Connect Seerr, Sonarr, Radarr and Bazarr

Seerr, Sonarr, Radarr and Bazarr talk to Jellyfin through its HTTP API.
Deadfin serves the same API, so you point them at Deadfin with the settings
they already have. This page tells you what each tool calls and what to
change.

There are two phases:

- **Coexistence.** Deadfin runs beside Jellyfin on the same media. Sonarr and
  Radarr notify both servers.
- **Cutover.** Deadfin replaces Jellyfin. Seerr and Bazarr move to Deadfin.

Do the import first (see [importing-from-jellyfin.md](importing-from-jellyfin.md)).

## Before you start

### Authentication

Deadfin accepts the same credentials as Jellyfin 12:

- the `Authorization: MediaBrowser Token="<key>"` header;
- the `ApiKey=<key>` query parameter.

The legacy forms are off by default, as in Jellyfin 12: the `X-Emby-Token`,
`X-MediaBrowser-Token` and `X-Emby-Authorization` headers and the `api_key`
query parameter. Seerr, Sonarr, Radarr and Bazarr all send the
`Authorization` header.

### API keys

Give each tool its own key. Then you can revoke one tool without the others.

1. In Deadfin, open Dashboard → API Keys.
2. Add a key with the tool name as the app name, for example `sonarr`.
3. Copy the key into the tool.

With the API (an admin token in the header):

```bash
curl -X POST 'http://<deadfin>:8096/Auth/Keys?App=sonarr' \
     -H 'Authorization: MediaBrowser Token="<admin token>"'
curl -s 'http://<deadfin>:8096/Auth/Keys' \
     -H 'Authorization: MediaBrowser Token="<admin token>"'
```

The import brings Jellyfin's API keys across. A key that a tool used with
Jellyfin is valid on Deadfin too.

### Paths

Deadfin acts on a reported path only when the path is inside a library
folder. It ignores other paths. When Sonarr or Radarr sees the media at a
different path than Deadfin, set a path map in the tool (see below).

Example: Sonarr mounts the media at `/data/media/tv`, and Deadfin mounts it
at `/media/tv`. Then the map is `/data/media/` → `/media/`.

## Coexistence: Sonarr and Radarr beside Jellyfin

Sonarr and Radarr tell the media server about each import, rename and
delete. Add a second connection for Deadfin. Leave the Jellyfin connection
as it is.

1. In Sonarr or Radarr, open Settings → Connect → **+** → **Emby / Jellyfin**.
2. Set these fields:

   | Field | Value |
   |---|---|
   | Name | `Deadfin` |
   | Host, Port, Use SSL, URL Base | Deadfin's address. URL Base only if Deadfin has a base URL. |
   | API Key | The Deadfin key for this app (`sonarr` or `radarr`). |
   | Send Notifications | **Off** |
   | Update Library | **On** |
   | Map Paths From / Map Paths To | The path map, if the paths differ. These are advanced fields: turn on Show Advanced. |

3. Click **Test**. The test calls `GET /System/Configuration`. A pass means
   that Deadfin accepts the key.
4. Save.

Keep **Send Notifications** off. It calls `POST /Notifications/Admin`, which
Jellyfin removed in 10.9 and which Deadfin does not serve. The app sends the
notification before the library update. The error from the notification
stops the event, and the library update is never sent.

### What happens on an import

1. Sonarr or Radarr calls `GET /Items` with `fields=Path,ProviderIds` to find
   the series or movie path in Deadfin.
2. It calls `POST /Library/Media/Updated` with the path.
3. Deadfin waits for a quiet period with no more reports. The period is
   `LibraryMonitorDelay` in `system.xml`, 60 seconds by default.
4. Deadfin then scans every library once (D-35). The scan skips unchanged
   items. A burst of imports gives one scan.

### Check

After the next import, open Dashboard → Scheduled Tasks. **Scan Media
Library** shows a run that started about one quiet period after the import.
The new item is then in the library.

If no scan runs, check the path map. The reported path must be inside a
Deadfin library folder.

### Undo

Delete the Deadfin connection in Sonarr or Radarr. Then revoke the key in
Deadfin (Dashboard → API Keys).

## Cutover

Do these steps when you stop Jellyfin for good. Sonarr and Radarr need one
more step: delete the Jellyfin connection.

### Seerr

Seerr reads the library from the media server, and it signs users in with
it. Seerr finds each user by their Jellyfin user ID. The import gives most
users new IDs in Deadfin (see
[importing-from-jellyfin.md](importing-from-jellyfin.md)), so you change the
IDs in Seerr's database first.

The steps:

1. Stop Seerr.
2. Back up Seerr's config directory.
3. Run the remap script below.
4. Start Seerr. In Settings → Jellyfin, set the host to Deadfin. Keep the API
   key: the imported key is valid on Deadfin. Save. Seerr then reads
   Deadfin's server ID for its "Play on" links.
5. In the same page, click **Sync Libraries**. Seerr stores each library
   by ID and name. Check that each library you want is on.
6. In Settings → Jobs, run **Jellyfin Full Library Scan** once. The scan
   stores Deadfin's item IDs for each request.
7. Sign in to Seerr as one remapped user. Check that the user sees their
   requests.

#### Remap script

The script works on Seerr's SQLite database (`db/db.sqlite3` in the config
directory, the default). It needs `curl`, `jq` and `sqlite3`.

The script does these steps:

1. It copies the config directory to a backup.
2. It reads Deadfin's users from `GET /Users` with an admin API key.
3. It matches each Seerr Jellyfin user to a Deadfin user by user name. The
   match ignores case.
4. It sets `jellyfinUserId` to the Deadfin ID (32 lowercase hex digits).
5. It prints each Seerr user that has no match, and does not change them.

```sh
#!/bin/sh
# Usage: DEADFIN_URL=http://deadfin:8096 DEADFIN_KEY=<admin API key> \
#        ./seerr-remap.sh /path/to/seerr/config
# Run with Seerr stopped.
set -eu
config=${1:?usage: seerr-remap.sh <seerr config directory>}
db="$config/db/db.sqlite3"
[ -f "$db" ] || { echo "no database at $db" >&2; exit 1; }

backup="$config.backup-$(date +%Y%m%d-%H%M%S)"
cp -a "$config" "$backup"
echo "backup: $backup"

users=$(mktemp)
trap 'rm -f "$users"' EXIT
curl -fsS -H "Authorization: MediaBrowser Token=\"$DEADFIN_KEY\"" \
  "$DEADFIN_URL/Users" |
  jq -r '.[] | [(.Name | ascii_downcase), .Id] | @tsv' >"$users"

tab=$(printf '\t')
sqlite3 -separator "$tab" "$db" \
  'SELECT id, jellyfinUsername FROM "user" WHERE jellyfinUserId IS NOT NULL;' |
  while IFS="$tab" read -r id name; do
    key=$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')
    new=$(awk -F "$tab" -v n="$key" '$1 == n { print $2; exit }' "$users")
    if ! printf '%s' "$new" | grep -Eq '^[0-9a-f]{32}$'; then
      echo "not matched: Seerr user $id ($name)"
      continue
    fi
    sqlite3 "$db" "UPDATE \"user\" SET jellyfinUserId = '$new' WHERE id = $id;"
    echo "remapped: $name -> $new"
  done
```

For a Seerr user that the script does not match, do one of these:

- Find the user's ID in Deadfin (Dashboard → Users, or `GET /Users`) and set
  `jellyfinUserId` in the `user` row by hand.
- Delete the user in Seerr and let them sign in again. Their old requests
  then stay with the deleted account.

#### What Seerr calls

Deadfin serves each of these:

| Use | Calls |
|---|---|
| Sign-in | `POST /Users/AuthenticateByName`, QuickConnect (`/QuickConnect/Initiate`, `/QuickConnect/Connect`, `/Users/AuthenticateWithQuickConnect`) |
| Settings | `GET /System/Info`, `GET /System/Info/Public`, `GET /Users`, `GET /Users/{id}`, `GET /Library/MediaFolders`, `/Auth/Keys` |
| Scans and availability | `GET /Items`, `GET /Items/Latest`, `GET /Shows/{id}/Seasons`, `GET /Shows/{id}/Episodes` |
| Avatars | `GET` and `HEAD /UserImage` |

On sign-out, Seerr calls `DELETE /Devices` with the legacy
`X-Emby-Authorization` header. Deadfin refuses it, as Jellyfin 12 does.
Seerr logs the error, and the sign-out completes.

### Bazarr

Bazarr's Jellyfin integration tells one server about new subtitle files.
Switch it to Deadfin at cutover.

1. In Bazarr, open Settings → Jellyfin.
2. Set **Server URL** to Deadfin and **API Key** to a Deadfin key named
   `bazarr`.
3. Choose the movie and series libraries again. Bazarr reads them from
   `GET /Library/VirtualFolders`.
4. Save.

After a subtitle download, Bazarr finds the item with `GET /Items` and
`GET /Shows/{id}/Episodes`. Then it calls
`POST /Items/{id}/Refresh?metadataRefreshMode=ValidationOnly`. Deadfin scans
the library that holds the item, and the new subtitle file shows up.

In the async mode, Bazarr calls `POST /Library/Media/Updated` instead. Then
Deadfin scans every library after the quiet period, as for Sonarr and
Radarr.

### Lidarr

Lidarr's **Emby / Jellyfin** connection does not work with Jellyfin 12 or
with Deadfin. It puts `/mediabrowser` in front of each path, and it sends
only the legacy `X-MediaBrowser-Token` header.

Deadfin finds new music with the scheduled library scan, every 12 hours by
default (Dashboard → Scheduled Tasks → Scan Media Library). To see new music
sooner, run the scan by hand or make the interval shorter.

### Plugins

Jellyfin plugins do not run in Deadfin (D-08). A tool that works through a
Jellyfin plugin, such as the Webhook plugin or Shokofin, has no Deadfin
equivalent.

## If something fails

- **Test fails with 401:** the key is wrong or revoked. Make a new key.
- **Import gives no scan:** turn off Send Notifications, and check the path
  map.
- **Seerr user gets "Access denied":** the user's `jellyfinUserId` is still
  the Jellyfin ID. Run the remap script again with Seerr stopped, or fix the
  row by hand.
- **Seerr shows no libraries:** click Sync Libraries and turn them on.

Report other failures as in [reporting-bugs.md](reporting-bugs.md). Include
the tool name and version, and remove API keys from the output.
