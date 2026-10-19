# Report a bug

Report bugs as GitHub issues at
<https://github.com/deadfin-org/deadfin/issues/new/choose>. Issues are
public: anyone can read them. Use the "Bug report" form for a fault and the
"Client compatibility report" form for a client test.

## Before you report

1. Check [known-differences.md](known-differences.md). The gap may be known.
2. Try the same action in Jellyfin, if you still have it. "Jellyfin does X,
   Deadfin does Y" is the most useful report.
3. Try a second client if you can. A fault in one client only can be a
   client defect. Report a deadfin-web defect in
   [deadfin-org/deadfin-web](https://github.com/deadfin-org/deadfin-web/issues)
   and a SharkParty defect in
   [deadfin-org/SharkParty](https://github.com/deadfin-org/SharkParty/issues).

## What to put in the report

- **Deadfin build**: the output of
  `curl -s http://<host>:8096/deadfin/v1/build`.
- **Client**: name, version, platform, and player (for Swiftfin: native or
  VLC player).
- **Steps**: what you did, in order. What you expected. What happened.
- **The item**: container, video and audio codecs, subtitle files. In the web
  client: item page → ⋯ → Media Info. Do not send the media file.
- **Server log** around the time of the fault:

  ```bash
  docker compose logs --since 10m deadfin > deadfin.log
  ```

  For playback faults, also attach the newest
  `ffmpeg-transcode-*.log` from
  `/var/lib/deadfin/log/`.
- For more detail, restart with `DEADFIN_LOG_LEVEL=debug`, repeat the steps,
  and send that log. Turn it off after.

## Remove private data

Logs and API responses can contain user names, device names, IP addresses,
file paths, and access tokens. Before you share a log in public:

- Remove every `Token=`, `api_key=`, `ApiKey=` and `X-Emby-Token` value.
- Replace user names and paths that you do not want to share.
- Do not send `deadfin.db` or anything from `config/`.

## Security problems

Report security problems privately through GitHub private vulnerability
reporting:
<https://github.com/deadfin-org/deadfin/security/advisories/new>. See
[SECURITY.md](../../SECURITY.md).
