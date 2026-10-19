# Accessibility and languages

Deadfin is a media server. People reach it through a client app, so most
accessibility depends on the client. The server supplies the media features
that clients need for captions, audio tracks and languages. This statement
covers the server. The web client and the Apple TV client have their own
statements:
[deadfin-web](https://github.com/deadfin-org/deadfin-web/blob/master/ACCESSIBILITY.md)
and
[SharkParty](https://github.com/deadfin-org/SharkParty/blob/main/ACCESSIBILITY.md).

## Choose the client that suits you

Deadfin speaks the Jellyfin API. Use the Jellyfin client that works best with
your assistive technology: jellyfin-web, Swiftfin, Jellyfin for Android and
Android TV, Infuse, or the bundled
[deadfin-web](https://github.com/deadfin-org/deadfin-web). The
[client matrix](docs/alpha/client-matrix.md) shows what each app was tested
with.

## Captions and subtitles

- Deadfin reads embedded subtitle streams and sidecar subtitle files.
- It extracts text subtitles from MKV files. ASS and SSA keep their styling;
  other text formats are served as SRT or converted to the format the client
  asks for.
- Each subtitle stream carries its language, its forced flag, and its SDH
  (hearing-impaired) flag. Clients show these flags in their track pickers.
- When a client cannot render a subtitle format, Deadfin burns the subtitles
  into the video during the transcode.
- Users with subtitle management permission can upload a subtitle file for an
  item.

## Audio tracks

- Clients list every audio track in a file, with its language and title. Pick
  any track, including an audio-description track where the file has one.
- Each user sets a preferred audio language and a preferred subtitle language,
  and a subtitle mode: Default, Always, Only forced, Smart, or None.
- Deadfin carries your audio and subtitle choice from one episode to the next
  episode of the same series. Pick Japanese audio with English subtitles once,
  and the next episode starts with the same choice. See D-36 in
  [docs/divergences.md](docs/divergences.md).

## Languages and regions

Deadfin carries the localization data of Jellyfin:

| Data | Source | Endpoint |
|---|---|---|
| ISO 639-2 languages | `internal/localization/assets/iso6392.txt` | `/Localization/Cultures` |
| Countries | `internal/localization/assets/countries.json` | `/Localization/Countries` |
| UI language options for clients | `internal/localization/assets/options.json` | `/Localization/Options` |
| Parental-rating systems for 44 countries | `internal/localization/assets/ratings/` | `/Localization/ParentalRatings` |

- Each library has a preferred metadata language and country. Providers fetch
  titles, overviews and ratings in that language and for that country.
- Search finds anime by its English, romaji or Japanese title. "Shingeki" and
  "進撃" both find "Attack on Titan". See D-48 in
  [docs/divergences.md](docs/divergences.md).
- The clients hold the translated UI strings. The UI language follows the
  client.

## Known limitations

- Text that the server makes itself is English: scheduled task names, the
  "Collections" library name, and activity log entries.
- Accessibility of menus, players and settings depends on the client you use.

## Report a barrier

Open an issue in
[deadfin-org/deadfin](https://github.com/deadfin-org/deadfin/issues/new/choose)
and put "Accessibility" in the title. Maintainers add the `accessibility`
label. Tell us:

- the client app and its version;
- the assistive technology you use, if any (screen reader, switch control,
  captions, magnifier);
- what you tried to do and what stopped you.

For a barrier inside the deadfin-web or SharkParty app, open the issue in that
app's repository.
