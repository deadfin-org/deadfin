# Apple TV playback QA matrix

This matrix is the playback check for Deadfin with tvOS clients before the
community alpha (v0.3.0). It exists to find defects like this one: an HLS
fMP4 copy-remux of MKV HEVC Main10 23.976 with AAC, where AVPlayer dropped
the audio for about 2 s at a time.

The matrix has three layers of evidence for each cell:

1. **Decision.** The server's PlaybackInfo answer for the real tvOS device
   profiles, compared with the expected decision.
2. **Automated pre-TV probe.** A server-side container check of what the
   client fetches (`qamatrix check`), and an AVFoundation playback probe on
   macOS (`avprobe`).
3. **Operator TV check.** A person watches the cell on a real Apple TV.

The machine-readable form is [`atv-matrix.json`](atv-matrix.json): cell →
expected decision → one run per mapped item (media summary, actual decision,
server check, probe result) → operator check. It holds no item IDs, titles or
URLs. The tool is [`tools/qamatrix`](../../tools/qamatrix).

## Players

| Key | Player | Device profile source |
|---|---|---|
| SP-AV | SharkParty, Native (AVPlayer) | SharkParty `subtitle-overlay` (PR #8): `VideoPlayerType+AVPlayer.swift`. External delivery for SRT, VTT, ASS, SSA and PGS, drawn by the overlay. |
| SP-VLC | SharkParty, VLC | `VideoPlayerType+VLC.swift`. Stock Swiftfin's default player is VLC with the same profile, so this column also stands for stock Swiftfin on its defaults. |
| SF-AV | Stock Swiftfin, Native (AVPlayer) | SharkParty `main` before 844ca958: the same profile as SP-AV, but text subtitles go to HLS WebVTT and PGS is burned in. |

[`tools/qamatrix/profiles.go`](../../tools/qamatrix/profiles.go) holds the
transcribed profiles with file references. The profiles assume an Apple TV 4K
(2nd or 3rd generation) on an HDR TV with the client defaults: HEVC, HDR10,
HLG and Dolby Vision decode; no AV1 or VP9 decode; no forced HDR, DV or
subtitle transcode; 120 Mbit/s.

SharkParty's Compatibility setting (Settings > Video player > Playback
quality > Device profile) must be Auto.

SharkParty does not send `PlayMethod` in its playback reports. On rc.9 the
server records a missing `PlayMethod` as DirectPlay and does not fill the
session `TranscodingInfo`, so the Session panel reads Direct Play for every
SharkParty session, also for an HLS remux or transcode (deadfin#635). Until
that fix ships, the Session panel does not prove the delivery path; the
server ingress log does: an HLS play shows `master.m3u8` and `hls1/main/N.mp4` requests, a direct play shows `/Videos/{id}/stream?static=true`.

## Method

A full cartesian product of the dimensions has more than a million cells. The
matrix uses:

- **Pairwise coverage.** Every value of every dimension appears in at least
  one cell with a library item, and the risky pairs are covered:
  container × video, video × player, audio × player and subtitle × player.
  The PlaybackInfo decision is recorded for all three players in every cell,
  so player × everything is covered at the decision layer.
- **Targeted high-risk cells.** Each cell names its risk. The regression
  cells pin the audio-dropout items: `qamatrix map -pin ATV-R01=ITEMID` names them,
  and later runs keep the pins from the local matrix. Without a pin, the
  cell's traits select the item.
- **Real items.** `qamatrix map` selects one or two library items per cell
  from the ffprobe-derived MediaStreams, prefers items long enough for the
  probe, and takes them from different series.

Delivery terms:

- **DirectPlay**: the client reads the file (`/Videos/{id}/stream?static=true`).
- **Remux**: HLS fMP4 with the video copied. The audio is copied or transcoded.
- **Transcode**: HLS with the video encoded (QSV on live).
- `a:tc` means the selected audio track is transcoded. `s:` is the subtitle
  delivery method: External, Embed, Hls (WebVTT in the manifest) or Encode
  (burned in, which forces a video transcode).

"Pass on the TV" for every stream cell: playback starts in under 5 s; there is
no audio dropout, A/V drift, stutter or green frame over the play time; seeks
land within 2 s of the target and resume audio and video together. A cell
adds its own condition where it has one (HDR mode, subtitle styling, channel
mapping).

## Interaction cells

The Video, Audio and Subtitles groups cross media traits. The Interaction
group (ATV-I01 to ATV-I40) crosses what the viewer does with how the server
delivers the stream. It exists for defects like the one in ATV-R03: on
v0.3.0-rc.8, a 4K HEVC Main10 file with E-AC-3 5.1 played cleanly from 0 on
SharkParty AVPlayer, but a resume at about 2:58 gave audio dropouts of
several seconds with smooth video.

Dimensions:

- **Delivery.** DirectPlay; Remux a:copy; Remux a:transcode; Transcode SDR
  (video encode, including burn-in); Transcode from HDR (tone map, or burn-in
  on an HDR source). The media traits and the player select the delivery.
- **Operation.** Start at 0; resume from a saved position off the 6 s grid;
  seek forward far; seek backward behind the running job; seek to the last
  30 s; audio-track switch mid-play; subtitle on, off or switch mid-play;
  pause for more than 5 min; background and foreground; next-episode
  autoplay; two concurrent streams.
- **Traits that change the outcome.** 4K; HDR10 and Dolby Vision (P5, P7,
  P8); E-AC-3 (with Atmos), TrueHD and DTS; image (PGS) and text (SRT, ASS)
  subtitles; VFR; runtime over 2 h.

Most operations on an HLS stream make the server stop one ffmpeg job and
start another at a new position. On a video copy, the new job can cut only
at source keyframes, so its segments must still match the playlist that the
client already holds. On an audio transcode, the encoder restarts with
priming samples. On a video transcode, keyframes follow the grid, but the
first segment after a restart takes seconds. Each cell names the mechanism
in its risk sentence.

Pruning. The full product is 5 deliveries × 11 operations × the traits. The
group covers every delivery × operation pair with one cell (a cell can pin
two operations when they share a mechanism), and adds named triples where a
trait changes the restart:

| Delivery | Start | Resume | Seek fwd | Seek back | Near end | Audio switch | Subtitle switch | Long pause | Background | Next episode | Two streams |
|---|---|---|---|---|---|---|---|---|---|---|---|
| DirectPlay | V01, A10 | I36 | I36 | I36 | I37 | I38 | I38 | I39 | I39 | I37 | I40 |
| Remux a:copy | I01, V02 | R03, I02, I03, I12, I13 | I04 | I05 | I06 | I07 | I14 | I08 | I09 | I10, O01 | I11 |
| Remux a:transcode | V05, A01 | I15 | I16 | I17 | I20 | I18 | I22 | I19 | I19 | I21 | I27 |
| Transcode SDR | V10, V13 | I23, I29 | I24 | I25 | I24 | I31 | I28 | I26 | O02 | I30 | I27 |
| Transcode from HDR | V07 (SP-VLC) | I32 | I33 | I33 | I24 | I31 | I28 | I34 | I34 | I30 | I35 |

For Transcode from HDR, near end, audio switch, subtitle switch and next
episode use the Transcode SDR cells: the restart is the same, and the tone
map adds only cost and color, which I32 to I35 check.

Named triples:

- 4K × E-AC-3 × resume: ATV-R03, with ATV-I01 (same shape from 0) and
  ATV-I02 (1080p resume) as controls that split media from restart.
- Dolby Vision 8.1 × E-AC-3 Atmos × resume: ATV-I03.
- 4K HDR × TrueHD transcode × resume: ATV-I15.
- 4K × PGS through the SharkParty overlay × resume: ATV-I13.
- VFR × resume: ATV-I12.
- Runtime over 150 min × 4K × seek past 90 min: ATV-I04.
- Audio switch that changes a:copy to a:transcode: ATV-I18.
- Stock Swiftfin × PGS × burn-in toggle: ATV-I28; × ASS burn-in × resume:
  ATV-I29; × 4K HDR × PGS burn-in × seeks: ATV-I33.
- H.264 Hi10P × dual audio × audio switch: ATV-I31.

`Entries` on a cell names the resume or seek points, in seconds (negative
counts from the end). `qamatrix check` fetches six segments from each point
in addition to the start and mid-file runs. The TV check is the evidence for
the operation itself: the probe and the check cannot pause, background or
autoplay. The two-stream cells need a second client.

The decisions of ATV-R03 and the Interaction cells come from v0.3.0-rc.8
(`map -cells`); the build in the matrix header names the results of the
other cells.

## Cells

The cell definitions live in
[`tools/qamatrix/cells.go`](../../tools/qamatrix/cells.go). This section is
generated from them by `qamatrix render`.

<!-- qamatrix:cells -->

#### Regression

| ID | Pins | Expected: SP-AV | SP-VLC | SF-AV | Why it is here |
|---|---|---|---|---|---|
| ATV-R01 | MKV HEVC Main10 SDR 23.976 AAC 2.0 + ASS with fonts | Remux a:copy s:External | DirectPlay a:copy s:Embed | Transcode a:copy s:Encode | HLS fMP4 copy-remux relaunched ffmpeg per segment, so AVPlayer dropped audio for about 2 s at a time. |
| ATV-R02 | MKV HEVC Main 8-bit 23.976 AAC 2.0, short episode | Remux a:copy | DirectPlay a:copy | Remux a:copy | Second item that showed the audio dropouts; short episode, so the probe covers it end to end. |
| ATV-R03 | MKV HEVC Main10 SDR 4K scope EAC3 5.1, resumed at 2:58 | Remux a:copy | DirectPlay a:copy | Remux a:copy | v0.3.0-rc.8: a resume at about 2:58 restarted ffmpeg with -ss/-start_number, the job cut segments on an hls_time grid from the restart point instead of the playlist's keyframe grid, and AVPlayer dropped audio for seconds at a time while video stayed smooth. |

#### Video

| ID | Pins | Expected: SP-AV | SP-VLC | SF-AV | Why it is here |
|---|---|---|---|---|---|
| ATV-V01 | MP4 H.264 High 1080p 23.976 AAC 2.0 | DirectPlay a:copy | DirectPlay a:copy | DirectPlay a:copy | Baseline direct play for all three players. |
| ATV-V02 | MKV H.264 High 1080p AC3 5.1 + embedded SRT | Remux a:copy s:External | DirectPlay a:copy s:Embed | Remux a:copy s:Hls | The most common library shape: H.264 copy-remux into fMP4 with AC3 copy. |
| ATV-V03 | MP4 HEVC Main 8-bit 1080p AAC | Remux a:copy | DirectPlay a:copy | Remux a:copy | AVPlayer requires codec tag hvc1/dvh1. Pinned upstream never fills CodecTag (MediaStreamInfo.cs:204 binds codec_tag_string?), so HEVC MP4 remuxes instead of direct play. Parity, not a defect. |
| ATV-V04 | MKV HEVC Main10 SDR 720p 29.97 EAC3 5.1 | Remux a:copy | DirectPlay a:copy | Remux a:copy | 29.97 copy-remux: segment boundaries on a non-film cadence; EAC3 copy into fMP4. |
| ATV-V05 | MKV HEVC HDR10 4K TrueHD Atmos 7.1 | Remux a:transcode | Remux a:transcode | Remux a:transcode | 4K HDR10 copy-remux at high bitrate with an audio transcode from TrueHD; HDR mode switch on the TV. |
| ATV-V06 | MKV HEVC HDR10+ 4K | Remux | DirectPlay | Remux | HDR10+ dynamic metadata survives the copy-remux only if the SEI is copied; the TV shows HDR10+ or plain HDR10. |
| ATV-V07 | Dolby Vision profile 5 (DOVI, no HDR10 fallback) | Remux | Transcode | Remux | P5 has no HDR10 base layer: AVPlayer must see a dvh1 sample entry, and VLC (no DOVI in its range list) gets a transcode that needs tone mapping or shows purple/green. |
| ATV-V08 | Dolby Vision profile 7 (DOVIWithEL) | Remux | DirectPlay | Remux | Dual-layer UHD BD remux. AVPlayer plays the base layer; the enhancement layer is dropped. |
| ATV-V09 | Dolby Vision profile 8.1 (DOVIWithHDR10) | Remux | DirectPlay | Remux | The common streaming DV shape; the remux must keep the RPU and tag dvh1 so AVPlayer engages DV. |
| ATV-V10 | MKV AV1 10-bit + Opus | Transcode a:transcode | Transcode a:copy | Transcode a:transcode | Apple TV 4K has no AV1 decode: a full video transcode (QSV on live). Opus is copied for VLC only. |
| ATV-V11 | VC-1 (Blu-ray remux) with DTS or AC3 | Transcode | DirectPlay | Transcode | Legacy codec: AVPlayer needs a full transcode; VLC plays it directly. With TrueHD first, VLC also transcodes the video, because HLS cannot carry VC-1 (StreamBuilder.cs:31 _supportedHlsVideoCodecs). |
| ATV-V12 | MPEG-2 video (DVD or broadcast) | Transcode | DirectPlay | Transcode | MPEG-2 in a program or transport stream; AVPlayer needs a transcode, often with deinterlace. |
| ATV-V13 | MKV H.264 High 10 (Hi10P anime) + FLAC | Transcode a:copy | Transcode a:copy | Transcode a:copy | No Apple decoder for H.264 10-bit, so both players transcode the video; FLAC audio. |
| ATV-V14 | MPEG-TS H.264 progressive + AAC | Remux a:copy | DirectPlay a:copy | Remux a:copy | TS source timestamps (non-zero start, PCR wrap) through an fMP4 remux. |
| ATV-V15 | AVI MPEG-4 ASP (XviD) + MP3 | Transcode a:transcode | DirectPlay a:copy | Transcode a:transcode | Legacy AVI: packed B-frames and VFR-like timestamps; HLS cannot carry MPEG-4 ASP, so AVPlayer transcodes. |
| ATV-V16 | HEVC Range Extensions (Rext, 4:2:2/4:4:4) | Transcode | Transcode | Transcode | Profile outside main/main 10: every player transcodes. |
| ATV-V17 | 4K HEVC 10-bit SDR | Remux | DirectPlay | Remux | 4K SDR copy-remux: high segment sizes and no HDR mode switch. |
| ATV-V18 | SD H.264 25 fps (PAL) | (any) | DirectPlay | (any) | PAL cadence and SD scaling; display frame-rate match on the TV. |
| ATV-V19 | 24.000 fps 1080p | (any) | DirectPlay | (any) | Exact 24p against the 23.976 majority; match-frame-rate on the TV. |
| ATV-V20 | 50 fps | (any) | DirectPlay | (any) | PAL broadcast/sport cadence. |
| ATV-V21 | 59.94 fps | (any) | DirectPlay | (any) | High frame rate: HEVC is capped at 60 fps in the AVPlayer profile; decode load and frame drops. |
| ATV-V22 | Variable frame rate (RealFrameRate differs from AverageFrameRate) | (any) | DirectPlay | (any) | VFR sources break fixed-length segment math and A/V sync in a copy-remux. |
| ATV-V24 | Anamorphic SD H.264 (DVD rip) | Transcode | Transcode | Transcode | Every tvOS profile rejects anamorphic H.264, so all players transcode to square pixels; the aspect ratio must survive. |
| ATV-V23 | Interlaced H.264 | Transcode | DirectPlay | Transcode | AVPlayer profile rejects interlaced H.264: a deinterlacing transcode (QSV deinterlace on live). |

#### Audio

| ID | Pins | Expected: SP-AV | SP-VLC | SF-AV | Why it is here |
|---|---|---|---|---|---|
| ATV-A01 | DTS 5.1 core | Remux a:transcode | DirectPlay a:copy | Remux a:transcode | AVPlayer cannot decode DTS: audio transcode in a video-copy remux. |
| ATV-A02 | DTS-HD MA 7.1 | any a:transcode | DirectPlay a:copy | any a:transcode | 8-channel transcode: channel layout and the AAC 7.1 path into AVPlayer. |
| ATV-A03 | TrueHD Atmos below 4K | any a:transcode | any a:transcode | any a:transcode | TrueHD is in no tvOS profile: every player transcodes audio; Atmos is lost. |
| ATV-A04 | EAC3 Atmos (DD+ JOC) | any a:copy | DirectPlay a:copy | any a:copy | EAC3 copy must keep the JOC extension so the receiver shows Atmos. |
| ATV-A05 | FLAC 2.0 in MKV | Remux a:copy | DirectPlay a:copy | Remux a:copy | FLAC copied into fMP4 HLS: AVPlayer support for fLaC in HLS is thin. |
| ATV-A06 | Opus 2.0 in MKV | Remux a:transcode | DirectPlay a:copy | Remux a:transcode | Opus is not in the AVPlayer profile: audio transcode beside a video copy. |
| ATV-A07 | Opus 5.1 in MKV | any a:transcode | DirectPlay a:copy | any a:transcode | Opus multichannel mapping (Vorbis order) into a 5.1 transcode. |
| ATV-A08 | MP3 2.0 in MKV | any a:transcode | DirectPlay a:copy | any a:transcode | MP3 is direct-playable only in MOV for AVPlayer; in MKV it transcodes. |
| ATV-A09 | AC3 mono | any a:copy | DirectPlay a:copy | any a:copy | Mono must play from the centre (or both fronts), not one side. |
| ATV-A10 | MP4 AAC 5.1 | DirectPlay a:copy | DirectPlay a:copy | DirectPlay a:copy | Multichannel AAC direct play. |
| ATV-A11 | HE-AAC (SBR, v2 where the library has it) | any a:copy | DirectPlay a:copy | any a:copy | Parametric stereo copied into fMP4; a wrong ASC plays mono or at half speed. |
| ATV-A12 | Dual audio: switch to the second track | Remux a:copy | DirectPlay a:copy | Remux a:copy | Swiftfin restarts playback with a new AudioStreamIndex; the remux must map the selected track. |
| ATV-A13 | Commentary track selected | (any) | (any) | (any) | A non-default secondary track; mapping by index, not by default flag. |
| ATV-A14 | PCM s16le in MKV | any a:transcode | DirectPlay a:copy | any a:transcode | PCM is direct-playable only from MOV for AVPlayer. |

#### Subtitles

| ID | Pins | Expected: SP-AV | SP-VLC | SF-AV | Why it is here |
|---|---|---|---|---|---|
| ATV-S01 | External SRT sidecar | any s:External | any s:External | (any) | Sidecar delivery and the subtitle switch/off path. |
| ATV-S02 | Embedded forced SRT | any s:External | any s:Embed | any s:Hls | Forced tracks must show without a selection (client) and deliver like any SRT (server). |
| ATV-S03 | ASS with embedded fonts (styled anime) | any s:External | any s:Embed | Transcode s:Encode | SharkParty overlay draws ASS with MKV fonts; stock Swiftfin burns it in (ASS cannot convert to WebVTT upstream). |
| ATV-S04 | PGS (Blu-ray image subtitles) | any s:External | any s:Embed | Transcode s:Encode | SharkParty gets the .sup as External; stock Swiftfin burns PGS in, a full transcode. |
| ATV-S05 | VobSub (DVD image subtitles) | Transcode s:Encode | any s:Embed | Transcode s:Encode | AVPlayer profiles burn DVD subtitles in: a full transcode with overlay. |
| ATV-S06 | Forced ASS | any s:External | any s:Embed | Transcode s:Encode | Signs-and-songs tracks are forced ASS; same delivery as ASS. |
| ATV-S07 | MP4 mov_text | any s:External | any s:Embed | any s:Hls | mov_text is in no AVPlayer profile: SharkParty converts it to External SRT; stock Swiftfin has no direct-play text path, so it goes to HLS with WebVTT in the manifest. |
| ATV-S08 | Subtitles off on a file with many tracks | Remux a:copy | DirectPlay a:copy | Remux a:copy | Off must not burn in or embed anything. |

#### Operations

| ID | Pins | Expected: SP-AV | SP-VLC | SF-AV | Why it is here |
|---|---|---|---|---|---|
| ATV-O01 | Next-episode autoplay across a remux | Remux a:copy | DirectPlay a:copy | Remux a:copy | End-of-stream on HLS, the next PlaybackInfo, and the resume point of the next episode. |
| ATV-O02 | Background and foreground during a transcode | Transcode | Transcode | Transcode | The server kills an idle HLS job after 60 s; returning from the home screen must restart cleanly. |
| ATV-O03 | HDR/SDR mode switch between items | Remux a:copy | DirectPlay a:copy | Remux a:copy | Going from an HDR10 item to an SDR item must return the TV to SDR. |
| ATV-O04 | Scrub with trickplay on a 4K remux | Remux a:copy | DirectPlay a:copy | Remux a:copy | Long scrubs on a remux restart ffmpeg at a far segment; trickplay thumbnails load. |

#### Interaction

| ID | Pins | Expected: SP-AV | SP-VLC | SF-AV | Why it is here |
|---|---|---|---|---|---|
| ATV-I01 | Remux a:copy, start at 0: 4K SDR 10-bit + EAC3 5.1 (control for R03) | Remux a:copy | DirectPlay a:copy | Remux a:copy | The same media as ATV-R03, started from 0: a pass here and a fail in R03 isolates the defect to the restart, not to the media. |
| ATV-I02 | Remux a:copy, resume off the 6 s grid: 1080p HEVC + EAC3/AC3 | Remux a:copy | DirectPlay a:copy | Remux a:copy | A resume restarts ffmpeg with -ss and -start_number, and the restarted job cuts segments from the restart point, not on the playlist's keyframe grid. 1080p separates the restart defect from 4K bitrate. |
| ATV-I03 | Remux a:copy, resume: Dolby Vision 8.1 + EAC3 Atmos | Remux a:copy | DirectPlay a:copy | Remux a:copy | High-risk triple. The restarted job must start on an IDR frame with its RPU, keep the dvh1 sample entry of the first job, and keep the JOC extension in the first EAC3 frame after the cut. |
| ATV-I04 | Remux a:copy, seek forward far: 4K HDR, runtime over 150 min | Remux a:copy | DirectPlay a:copy | Remux a:copy | A jump of more than 60 min into a large 4K file: a high -start_number, a long input seek, and a segment index far from the running job, which the server must kill and replace. |
| ATV-I05 | Remux a:copy, seek backward behind the job: 4K HEVC | Remux a:copy | DirectPlay a:copy | Remux a:copy | After a forward seek, a seek back to segments the current job never made needs a second restart; old and new jobs must not both write the same segment numbers. |
| ATV-I06 | Remux a:copy, seek to the last 30 s: MKV HEVC episode | Remux a:copy | DirectPlay a:copy | Remux a:copy | The restart seek clamps to runtime - 5 s (#482); the last segment is short, and end of stream must reach the client as an ENDLIST, not a stall. |
| ATV-I07 | Remux a:copy, audio-track switch mid-play: two AAC/AC3/EAC3 tracks | Remux a:copy | DirectPlay a:copy | Remux a:copy | Swiftfin restarts playback with a new AudioStreamIndex at the current position: a resume off the grid with a different -map. The new job must not reuse the old job's segments. |
| ATV-I08 | Remux a:copy, pause over 5 min then resume: 4K | Remux a:copy | DirectPlay a:copy | Remux a:copy | The server reaps the idle HLS job; on play, AVPlayer asks for the next segment and the server restarts at that segment: the resume path without a new PlaybackInfo. |
| ATV-I09 | Remux a:copy, background and foreground: Dolby Vision 8.1 | Remux a:copy | DirectPlay a:copy | Remux a:copy | tvOS tears down the player in the background; on return the app rebuilds the item at the saved position, so the TV leaves and re-enters DV around a restarted job. |
| ATV-I10 | Remux a:copy, next-episode autoplay: HEVC + EAC3 series | Remux a:copy | DirectPlay a:copy | Remux a:copy | The old job must stop and the new one start from 0 with a new play session; a stale job or a reused transcode directory gives the next episode the old media. |
| ATV-I11 | Two concurrent 4K remux streams (two devices) | Remux a:copy | DirectPlay a:copy | Remux a:copy | Two jobs share the transcode directory, disk and the job table (#514 had gaps and 500s). One stream's seek must not kill or poison the other. |
| ATV-I12 | Remux a:copy, resume: variable frame rate | Remux | DirectPlay | Remux | On VFR video, segment math from a frame rate is wrong, so a restart lands farther from the playlist grid than on CFR. |
| ATV-I13 | Remux a:copy + PGS on, resume: 4K | Remux a:copy s:External | DirectPlay a:copy s:Embed | Transcode s:Encode | The SharkParty overlay reads the external .sup and must place cues at the resumed media time, not at the restart's zero. |
| ATV-I14 | Remux a:copy, subtitle switch mid-play: text to image to off | Remux a:copy s:External | DirectPlay a:copy s:Embed | Remux a:copy s:Hls | On SharkParty the switch is client-side (External), so the stream must not restart; a restart here means the client sent a new PlaybackInfo. |
| ATV-I15 | Remux a:transcode, resume: 4K HDR + TrueHD Atmos | Remux a:transcode | Remux a:transcode | Remux a:transcode | High-risk triple. Two clocks restart together: the copied video cuts at a keyframe, the encoded audio starts with encoder priming. Any offset between them is a gap at every segment edge. |
| ATV-I16 | Remux a:transcode, seek forward far: DTS-HD MA, long runtime | Remux a:transcode | DirectPlay a:copy | Remux a:transcode | A long input seek with a DTS decoder restart; the first audio frames after -ss can be dropped, so audio starts late against video. |
| ATV-I17 | Remux a:transcode, seek backward: DTS 5.1 | Remux a:transcode | DirectPlay a:copy | Remux a:transcode | A backward seek restarts both the copy and the audio encoder on segments the client already played; AVPlayer must not mix cached and new segments. |
| ATV-I18 | Audio switch from a copy track to a transcode track: EAC3/AC3 to TrueHD/DTS | Remux a:transcode | (any) | Remux a:transcode | The delivery changes mid-play: the new job transcodes audio where the old copied it, at an off-grid position, and the audio codec in the init segment changes. |
| ATV-I19 | Remux a:transcode, pause over 5 min and background: Opus or DTS | Remux a:transcode | DirectPlay a:copy | Remux a:transcode | The reaped job restarts the audio encoder at the paused segment; a stale encoder or a reused segment gives a gap at the resume point. |
| ATV-I20 | Remux a:transcode, seek to the last 30 s: TrueHD | Remux a:transcode | Remux a:transcode | Remux a:transcode | The audio encoder flushes at end of input; a short last segment or a missing final audio packet stalls AVPlayer before the end. |
| ATV-I21 | Remux a:transcode, next-episode autoplay: Opus or FLAC series | Remux a:transcode | DirectPlay a:copy | Remux a:transcode | Episode-to-episode job handover while an audio encoder runs; the new episode must start from 0 with its own audio. |
| ATV-I22 | Text subtitle switch on stock Swiftfin: HLS WebVTT, DTS | Remux a:transcode s:External | DirectPlay a:copy s:Embed | Remux a:transcode s:Hls | Stock Swiftfin carries text subtitles as WebVTT in the manifest; a switch sends a new PlaybackInfo, a new job at the current position, and a new subtitle playlist that must line up with the restarted media. |
| ATV-I23 | Transcode, resume: H.264 Hi10P anime | Transcode | Transcode | Transcode | A transcode forces keyframes on the segment grid, so the grid holds; the risk is restart latency (6-8 s for the first segment at 1080p QSV) and the encoder's first GOP after -ss. |
| ATV-I24 | Transcode, seek forward far then to the end: AV1, VC-1 or MPEG-2 | Transcode | (any) | Transcode | Each far seek restarts the encoder; near the end the encoder must flush its last GOP so the final segment exists. |
| ATV-I25 | Transcode, seek backward: interlaced (deinterlace) | Transcode | (any) | Transcode | Deinterlace doubles or keeps the field rate; after a restart the output frame rate and timestamps must match the first job. |
| ATV-I26 | Transcode, pause over 5 min then resume: AV1 | Transcode | Transcode | Transcode | The reaped encoder job restarts at the paused segment; QSV session setup adds latency, and the client may time out the segment request. |
| ATV-I27 | Two concurrent streams: a video transcode and a 4K audio-transcode remux | Transcode | Transcode | Transcode | A QSV transcode and a CPU audio encode at once: if either runs below realtime, the client stalls; the two jobs must stay independent on seek. |
| ATV-I28 | Burn-in toggle on stock Swiftfin: PGS on and off mid-play | Remux a:copy s:External | DirectPlay a:copy s:Embed | Transcode s:Encode | Turning PGS on changes the delivery from Remux to a burn-in Transcode at the current position, and off changes it back: two method changes and two off-grid restarts. |
| ATV-I29 | Burn-in, resume on stock Swiftfin: ASS with fonts | any s:External | any s:Embed | Transcode s:Encode | With -ss before the input, the subtitles filter sees timestamps from 0, so burned cues can shift by the resume offset unless the job keeps the source timestamps. |
| ATV-I30 | Transcode, next-episode autoplay: Hi10P anime with ASS | Transcode s:External | Transcode | Transcode s:Encode | Encoder handover between episodes and font extraction for the next file; SharkParty must load the next episode's ASS track. |
| ATV-I31 | Transcode, dual audio switch: Hi10P anime Japanese to English | Transcode | Transcode | Transcode | A common anime shape: the audio switch restarts the full video transcode at the current position, with the restart latency of a seek. |
| ATV-I32 | Tone map, resume: Dolby Vision 5 on SharkParty VLC | Remux | Transcode | Remux | VLC has no DOVI in its range list, so P5 transcodes with a tone map: a 4K decode, reshape and tone map must start within the client timeout and keep correct colors after the restart. |
| ATV-I33 | Tone map, seek forward and back: 4K HDR10 + PGS burn-in on stock Swiftfin | Remux s:External | (any) | Transcode s:Encode | High-risk triple. Burn-in on a 4K HDR source is the heaviest job: 4K decode, overlay, tone map or HDR encode on the iGPU. It runs near realtime, so every seek risks a stall. |
| ATV-I34 | Tone map, pause and background: Dolby Vision 5 on SharkParty VLC | Remux | Transcode | Remux | The tone-map job is reaped during the pause; the restart must use the same tone-map settings, and the TV must stay in SDR. |
| ATV-I35 | Two concurrent streams: a 4K tone map and a 4K remux | Remux | Transcode | Remux | The heaviest GPU job beside the heaviest I/O job: the tone map must stay above realtime. |
| ATV-I36 | DirectPlay, resume and seek far on SharkParty VLC: 4K DV profile 7 + DTS-HD MA | Remux a:transcode | DirectPlay a:copy | Remux a:transcode | A large UHD BD remux read by byte ranges: VLC seeks with the MKV Cues; a missing or late Cues element makes each seek read far into the file. |
| ATV-I37 | DirectPlay, seek near end and next episode: MP4 H.264 episode | DirectPlay a:copy | DirectPlay a:copy | DirectPlay a:copy | A byte-range read at the end of the file (moov at the end needs an extra range); end of item reported by playstate, then the next PlaybackInfo. |
| ATV-I38 | DirectPlay, audio and subtitle switch on SharkParty VLC: DTS + a second track, PGS + SRT | any s:External | DirectPlay a:copy s:Embed | (any) | VLC switches tracks inside the file; the server sees only playstate. The chosen indexes must reach the saved user data for the next resume. |
| ATV-I39 | DirectPlay, pause over 5 min and background: MP4 H.264 movie | DirectPlay a:copy | DirectPlay a:copy | DirectPlay a:copy | No server job, so this isolates the client: an idle HTTP connection closes and AVPlayer must reopen the range at the paused offset. |
| ATV-I40 | Two concurrent 4K direct plays on SharkParty VLC | Remux a:transcode | DirectPlay a:copy | Remux a:transcode | Two high-bitrate byte-range readers on one disk and one uplink; a seek on one must not starve the other. |

<!-- /qamatrix:cells -->

### Coverage

<!-- qamatrix:coverage -->

| Dimension | Value | Cells with a library item |
|---|---|---|
| container | AVI | ATV-V15 |
| container | MKV | ATV-R01, ATV-R02, ATV-R03, ATV-V02, ATV-V04, ATV-V05, ATV-V06, ATV-V07, ATV-V08, ATV-V09, ATV-V10, ATV-V13, ATV-I01, ATV-I02, ATV-I03, ATV-I06, ATV-I07 |
| container | MP4 | ATV-V01, ATV-V03, ATV-A10, ATV-S07, ATV-I37, ATV-I39 |
| container | TS/M2TS | ATV-V14 |
| container | TS/MPEG-PS | ATV-V12 |
| video | AV1 | ATV-V10, ATV-I26 |
| video | AV1/VC-1/MPEG-2 | ATV-I24 |
| video | Dolby Vision P5 | ATV-V07, ATV-I32, ATV-I34 |
| video | Dolby Vision P7 | ATV-V08, ATV-I36 |
| video | Dolby Vision P8 | ATV-V09, ATV-I03, ATV-I09 |
| video | H.264 | ATV-V01, ATV-V02, ATV-V14, ATV-V18, ATV-I37 |
| video | H.264 10-bit | ATV-V13, ATV-I23, ATV-I30, ATV-I31 |
| video | H.264 anamorphic | ATV-V24 |
| video | H.264 interlaced | ATV-V23, ATV-I25 |
| video | HDR10 | ATV-V05, ATV-I33 |
| video | HDR10+ | ATV-V06 |
| video | HDR10/DV | ATV-I04, ATV-I15 |
| video | HEVC | ATV-I02, ATV-I05, ATV-I06, ATV-I10 |
| video | HEVC 10-bit SDR | ATV-R01, ATV-R03, ATV-V04, ATV-V17, ATV-I01 |
| video | HEVC 8-bit | ATV-R02, ATV-V03 |
| video | HEVC Rext | ATV-V16 |
| video | MPEG-2 | ATV-V12 |
| video | MPEG-4 ASP | ATV-V15 |
| video | VC-1 | ATV-V11 |
| res | 1080p | ATV-R01, ATV-R02, ATV-V01, ATV-V02, ATV-V03, ATV-V19, ATV-I02 |
| res | 4K | ATV-R03, ATV-V05, ATV-V06, ATV-V07, ATV-V08, ATV-V09, ATV-V17, ATV-I01, ATV-I03, ATV-I04, ATV-I05, ATV-I08, ATV-I09, ATV-I11, ATV-I13, ATV-I15, ATV-I32, ATV-I33, ATV-I35, ATV-I36, ATV-I40 |
| res | 720p | ATV-V04 |
| res | SD | ATV-V12, ATV-V15, ATV-V18, ATV-V24 |
| fps | 23.976 | ATV-R01, ATV-R02, ATV-R03, ATV-V01, ATV-V02, ATV-V03, ATV-V05 |
| fps | 24 | ATV-V19 |
| fps | 25 | ATV-V18 |
| fps | 29.97 | ATV-V04 |
| fps | 59.94 | ATV-V21 |
| fps | VFR | ATV-V22, ATV-I12 |
| audio | AAC | ATV-V03, ATV-V14 |
| audio | AAC 2.0 | ATV-R01, ATV-R02, ATV-V01 |
| audio | AAC 5.1 | ATV-A10 |
| audio | AC3 1.0 | ATV-A09 |
| audio | AC3 5.1 | ATV-V02 |
| audio | DTS | ATV-I17, ATV-I22 |
| audio | DTS 5.1 | ATV-A01 |
| audio | DTS-HD MA | ATV-I16, ATV-I36 |
| audio | DTS-HD MA 7.1 | ATV-A02 |
| audio | EAC3 | ATV-I04, ATV-I10 |
| audio | EAC3 5.1 | ATV-R03, ATV-V04, ATV-I01 |
| audio | EAC3/AC3 | ATV-I02 |
| audio | EAC3/Atmos | ATV-A04, ATV-I03 |
| audio | FLAC | ATV-V13 |
| audio | FLAC 2.0 | ATV-A05 |
| audio | HE-AACv2 | ATV-A11 |
| audio | MP3 | ATV-V15, ATV-A08 |
| audio | Opus | ATV-V10 |
| audio | Opus 2.0 | ATV-A06 |
| audio | Opus 5.1 | ATV-A07 |
| audio | Opus/DTS | ATV-I19 |
| audio | Opus/FLAC | ATV-I21 |
| audio | PCM | ATV-A14 |
| audio | TrueHD | ATV-I20 |
| audio | TrueHD/Atmos | ATV-A03, ATV-I15 |
| audio | TrueHD/Atmos 7.1 | ATV-V05 |
| audio | commentary | ATV-A13 |
| audio | multiple tracks | ATV-A12, ATV-I07, ATV-I18, ATV-I31, ATV-I38 |
| subs | ASS with fonts | ATV-R01, ATV-S03, ATV-I29, ATV-I30 |
| subs | PGS | ATV-S04, ATV-I13, ATV-I28, ATV-I33 |
| subs | SRT + PGS | ATV-I14, ATV-I38 |
| subs | VobSub | ATV-S05 |
| subs | embedded SRT | ATV-V02 |
| subs | external SRT | ATV-S01 |
| subs | forced | ATV-S02 |
| subs | forced ASS | ATV-S06 |
| subs | mov_text | ATV-S07 |
| subs | none | ATV-R02, ATV-R03, ATV-V01 |
| subs | none (off) | ATV-S08 |
| subs | two SRT tracks | ATV-I22 |
| delivery | DirectPlay | ATV-I36, ATV-I37, ATV-I38, ATV-I39, ATV-I40 |
| delivery | Remux a:copy | ATV-R03, ATV-I01, ATV-I02, ATV-I03, ATV-I04, ATV-I05, ATV-I06, ATV-I07, ATV-I08, ATV-I09, ATV-I10, ATV-I11, ATV-I12, ATV-I13, ATV-I14 |
| delivery | Remux a:copy to a:transcode | ATV-I18 |
| delivery | Remux a:transcode | ATV-I15, ATV-I16, ATV-I17, ATV-I19, ATV-I20, ATV-I21, ATV-I22 |
| delivery | Remux to Transcode (burn-in) | ATV-I28 |
| delivery | Transcode HDR (burn-in) | ATV-I33 |
| delivery | Transcode HDR to SDR | ATV-I32, ATV-I34 |
| delivery | Transcode HDR to SDR + Remux a:copy | ATV-I35 |
| delivery | Transcode SDR | ATV-I23, ATV-I24, ATV-I25, ATV-I26, ATV-I30, ATV-I31 |
| delivery | Transcode SDR (burn-in) | ATV-I29 |
| delivery | Transcode SDR + Remux a:transcode | ATV-I27 |
| ops | 10 min play, seeks | ATV-R01 |
| ops | HDR mode switch | ATV-O03 |
| ops | audio-track switch | ATV-A12 |
| ops | audio-track switch mid-play | ATV-I07, ATV-I18, ATV-I31 |
| ops | audio-track switch mid-play, subtitle on/off/switch | ATV-I38 |
| ops | background/foreground | ATV-I09 |
| ops | background/foreground, pause/resume | ATV-O02 |
| ops | full play, seeks | ATV-R02 |
| ops | long pause | ATV-I08, ATV-I26 |
| ops | long pause, background/foreground | ATV-I19, ATV-I34, ATV-I39 |
| ops | next-episode autoplay | ATV-I10, ATV-I21, ATV-I30 |
| ops | next-episode autoplay, resume from position | ATV-O01 |
| ops | resume off-grid | ATV-R03, ATV-I02, ATV-I03, ATV-I12, ATV-I13, ATV-I15, ATV-I23, ATV-I29, ATV-I32 |
| ops | resume off-grid, seek forward far, seek backward | ATV-I36 |
| ops | scrub | ATV-O04 |
| ops | seek backward | ATV-I05, ATV-I17, ATV-I25 |
| ops | seek forward far | ATV-I04, ATV-I16 |
| ops | seek forward far, seek backward | ATV-I33 |
| ops | seek forward far, seek near end | ATV-I24 |
| ops | seek near end | ATV-I06, ATV-I20 |
| ops | seek near end, next-episode autoplay | ATV-I37 |
| ops | start at 0 | ATV-I01 |
| ops | subtitle on/off/switch | ATV-I14, ATV-I22, ATV-I28 |
| ops | subtitle switch and off | ATV-S08 |
| ops | two concurrent streams | ATV-I11, ATV-I27, ATV-I35, ATV-I40 |
| player | SF-AV | ATV-I22, ATV-I28, ATV-I29, ATV-I33 |
| player | SP-AV | ATV-R03, ATV-I01, ATV-I02, ATV-I03, ATV-I04, ATV-I05, ATV-I06, ATV-I07, ATV-I08, ATV-I09, ATV-I10, ATV-I11, ATV-I12, ATV-I13, ATV-I14, ATV-I15, ATV-I16, ATV-I17, ATV-I18, ATV-I19, ATV-I20, ATV-I21, ATV-I23, ATV-I24, ATV-I25, ATV-I26, ATV-I27, ATV-I30, ATV-I31, ATV-I37, ATV-I39 |
| player | SP-VLC | ATV-I32, ATV-I34, ATV-I35, ATV-I36, ATV-I38, ATV-I40 |
| runtime | over 120 min | ATV-I16 |
| runtime | over 150 min | ATV-I04 |

<!-- /qamatrix:coverage -->

### Cells without a library item

<!-- qamatrix:missing -->

- **ATV-V20** 50 fps. Generate a sample, or have the operator source a real one:

  ```bash
  ffmpeg -f lavfi -i testsrc2=size=1920x1080:rate=50 -f lavfi -i sine=f=440:sample_rate=48000 -t 900 -c:v libx264 -profile:v high -pix_fmt yuv420p -c:a aac -b:a 160k sample-h264-50p.mkv
  ```

<!-- /qamatrix:missing -->

## Automated pre-TV probe

### Server check (`qamatrix check`)

For each item and player, the check fetches what the client would fetch:

- **HLS (Remux, Transcode).** The master and media playlists, the init
  segment, the first six media segments, and six segments after a seek to
  mid-file. It parses the fMP4 boxes (`tfdt`, `trun`) and checks that every
  track's decode timeline is continuous across segments, within 50 ms, and
  near its playlist time. Playlist drift fails a transcode only. A video
  remux without keyframe data gets equal-length segments, and ffmpeg stream
  copy cuts at source keyframes, so upstream drifts the same way; the check
  records remux drift as a note. It also checks that the HEVC sample entry is `hvc1`
  or `dvh1`, because AVPlayer does not play `hev1` in HLS. This is the check
  that catches the audio-dropout defect: there, a segment carried the media of the previous one.
  A cell with `Entries` (a resume or seek point) adds one run of six
  segments from each point, labelled `entry N s` in the trace.
- **DirectPlay.** A byte-range request must return `Content-Range`.
- **External subtitles.** The `DeliveryUrl` must return a well-formed file
  (`[Script Info]` for ASS, cue timings for SRT, a `PG` segment for PGS).

The check calls `DELETE /Videos/ActiveEncodings` after each HLS stream.

### AVFoundation probe (`avprobe`)

[`tools/qamatrix/avprobe`](../../tools/qamatrix/avprobe) is a macOS command
line tool. It plays each AVPlayer stream URL for the cell's probe time with
the operations of the cell: seeks of +10 s and -10 s, +5 min and -5 min where
the runtime allows, and a 5 s pause. Seeks use zero tolerance, as
`MediaPlayerProxy+AVPlayer.swift` does. It reports:

- time to first video frame;
- stalls (`AVPlayerItemPlaybackStalled`, and time spent waiting for data);
- timeline jumps: item time against host time while playing, and
  `AVPlayerItemTimeJumped` that no seek caused;
- decoded video frames through `AVPlayerItemVideoOutput`: gaps over 0.25 s
  and presentation times that go backwards;
- audio continuity through an `MTAudioProcessingTap` for progressive MP4.
  AVFoundation ignores an audio mix on HLS items, so for HLS the server
  check's audio-track timeline is the audio evidence;
- seek landing error and time to the next frame;
- the `AVPlayerItem` access log and error log.

The probe runs on macOS, not tvOS. It models the AVPlayer clients (SP-AV and
SF-AV) only, and it cannot open MKV or AVI, so VLC direct play is covered by
the server check and the operator.

### Map the cells to your library

The cells name traits, not items. `qamatrix map` reads your library through
the API and picks, for each cell, one or two items whose MediaStreams match
the cell's traits (see "Cells" above). It writes the mapping to
`docs/qa/atv-matrix.local.json` (or `-matrix`, or `$QAMATRIX_LOCAL`). Git
ignores that file: it holds your item IDs, titles and stream URLs. Every
command updates the local file, then writes the public `atv-matrix.json`
without them.

- `qamatrix items [-cells ATV-V23]` prints the mapped items of each cell, so
  you can find them on the TV.
- A cell that your library cannot fill shows under "Cells without a library
  item", with an ffmpeg command for a sample where one is possible.
- Pin a regression cell to a specific item with `-pin CELL=ITEMID`.
- Map only some cells with `-cells ATV-I01,ATV-I02`. The other cells keep
  their items and results, and the matrix keeps the server version of those
  results.

### Run it

```bash
# Set QAMATRIX_TOKEN to an API key for the server under test.
go run ./tools/qamatrix map -server https://deadfin.example      # map cells to items, record PlaybackInfo decisions
go run ./tools/qamatrix check -server https://deadfin.example    # server-side stream checks
go run ./tools/qamatrix plan -server https://deadfin.example > plan.json
```

On a Mac with Xcode:

```bash
cd tools/qamatrix/avprobe && swift build -c release
printf '%s' "$QAMATRIX_TOKEN" | .build/release/avprobe --plan plan.json --out probe.json
```

Then merge and render:

```bash
go run ./tools/qamatrix merge -probe probe.json
go run ./tools/qamatrix render
```

`avprobe` reads the key from stdin. The tools remove `ApiKey` from every URL
they write and redact the key from errors.

## Regression history

The regression cells on each server build (`qamatrix check`; `avprobe` on
macOS AVFoundation). "Item 1" and "item 2" are the cell's mapped items, in
the order of the results table:

| Cell | Player | v0.2.30 | v0.2.31 | v0.2.32 | v0.2.33 |
|---|---|---|---|---|---|
| ATV-R01 | SP-AV Remux | check fail: video overlap 2.5 s at seg 1; probe fail: 6 video gaps, max 4.4 s | check pass; probe pass, seeks 0.13-0.19 s | check pass; probe pass | check pass; probe pass |
| ATV-R01 | SF-AV Transcode (ASS burn-in) | check fail: audio gap 84 ms per segment; probe fail: waited 415.7 s for data | check pass; probe fail: a seek took 14.4 s, -12889 no response for map | check pass; probe fail: first frame after 11.8 s, one -12889 no response for map | check pass; probe: plays, seeks 0.12-0.15 s, one -12889 no response for media file |
| ATV-R02 | SP-AV Remux | check fail: video overlap 1.4 s at seg 1; probe fail: 7 video gaps, max 3.1 s | check pass; probe pass | check pass; probe pass | check pass; probe pass |
| ATV-S01 sidecar SRT | SF-AV Remux | not run | check fail: init segment HTTP 500 | check pass; probe: duplicate subtitle NAME, see Notes | same as v0.2.32 |

v0.2.34, targeted rerun, stock Swiftfin burn-in and ATV-V22:

| Cell | Player | v0.2.33 | v0.2.34 |
|---|---|---|---|
| ATV-R01 | SF-AV burn-in | first frame 5.9 s | first frame 5.9 s; seeks resume in at most 3.0 s; one -12889 |
| ATV-S03, ATV-S06 item 1 | SF-AV burn-in | no frame in 30 s (init timed out) | first frame 2.5-2.7 s; +300 s seek resumes in 7.7 s |
| ATV-S03, ATV-S06 item 2 | SF-AV burn-in | not probed | first frame 2.4-3.2 s; +300 s seek resumes in 7.5-8.0 s |
| ATV-V22 | SP-AV QSV transcode | 10 s segments under 6 s EXTINF | 6.006 s segments under 3.003 s EXTINF; check fail |

v0.2.35, targeted rerun. The "+300 s seek" column is the time from the seek to the first new frame:

| Cell | Item | Player | v0.2.33 | v0.2.35 |
|---|---|---|---|---|
| ATV-V22 | 1080p QSV transcode | SP-AV | 10 s segments under 6 s EXTINF | segments match EXTINF (3.003 s); check pass |
| ATV-V11, V12, V15, S05 | | SP-AV transcode | +300 s seek 6-8 s, -12889 | +300 s seek 0.2-0.5 s; pass |
| ATV-V10 | AV1 source | SP-AV transcode | +300 s seek about 6 s | +300 s seek 8.2 s, -300 s 6.1 s; -12889 |
| ATV-O02 | AV1 source | SP-AV transcode | -12889 | a seek took 23.2 s, 1 timeline jump |
| ATV-V13 | item 1 | SP-AV transcode | +300 s seek 8.4 s | +300 s seek 4.9 s |
| ATV-V16 | HEVC Rext 540p source | SP-AV transcode | -12889 | a seek took 28.3 s, 1 timeline jump |
| ATV-V23 | item 1 | SP-AV transcode | -12889 | seeks within 2.3 s; -12318 segment exceeds variant bandwidth |
| ATV-S04 | HEVC DV 1600p source with PGS | SF-AV PGS burn-in | a seek took 24.4 s | +300 s seek 18.2 s, max 25.8 s, 2 timeline jumps; segments 3.003 s, about 10-14 MB |
| ATV-R01 | | SF-AV burn-in | first frame 5.9 s | first frame 3.2 s; seeks within 0.45 s; pass |
| ATV-S03, S06 | items 1 and 2 | SF-AV burn-in | no frame in 30 s | first frame 2.3-3.2 s; seeks within 2.7 s |
| ATV-V06, V14, A13, O04 | remux | SP-AV | -12889 | pass; seeks within 1.3 s |

## Results

Operators: fill the "Operator TV check" column in the local matrix
(`cells[].operator`: `status` pass, fail or n/a; `notes`; `by`; `date`) and
run `qamatrix render`, which also updates `atv-matrix.json`. Mark the result on the real Apple TV, not on the probe.

<!-- qamatrix:results -->

Deadfin v0.2.33 54ff98c07269931c48b15f10e404e879e7fd0a81 (API 12.0.0), mapped 2026-10-17T01:57:48Z.

| Cell | Source | Actual: SP-AV | SP-VLC | SF-AV | Server check | AVPlayer probe | Operator TV check |
|---|---|---|---|---|---|---|---|
| ATV-R01 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pass | SP-AV pass, TTFF 0.8 s; SF-AV pass, TTFF 3.2 s |  |
| ATV-R02 | MKV hevc Main 8-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.8 s |  |
| ATV-R03 | MKV hevc Main 10 10-bit SDR 1512p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-V01 | MOV h264 High 8-bit SDR 818p 23.976 | DirectPlay | DirectPlay | DirectPlay | pass | SP-AV pass, TTFF 0.1 s |  |
| ATV-V01 | MOV h264 High 8-bit SDR 816p 23.976 | DirectPlay | DirectPlay | DirectPlay | pass | pending |  |
| ATV-V02 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pass | SP-AV pass, TTFF 1.0 s; SF-AV pass, TTFF 0.8 s |  |
| ATV-V02 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pass | pending |  |
| ATV-V03 | MOV hevc Main 8-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.8 s |  |
| ATV-V03 | MOV hevc Main 8-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-V04 | MKV hevc Main 10 10-bit SDR 720p 29.970 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.9 s |  |
| ATV-V05 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux, a:tc | Remux, a:tc | Remux, a:tc | pass | SP-AV pass, TTFF 2.7 s |  |
| ATV-V05 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux, a:tc | Remux, a:tc | Remux, a:tc | pass | SP-AV pass, TTFF 2.5 s |  |
| ATV-V06 | MOV hevc Main 10 10-bit HDR10Plus 2160p 23.976 | Remux | DirectPlay | Remux | pass | **SP-AV fail** no video frame within 30 s (status 2): The operation couldn’t be completed. (CoreMediaEr… |  |
| ATV-V06 | MKV hevc Main 10 10-bit HDR10Plus 1600p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.0 s |  |
| ATV-V07 | MKV hevc Main 10 10-bit DOVI 2160p 23.976 | Remux | Transcode | Remux | pass | SP-AV pass, TTFF 1.6 s |  |
| ATV-V07 | MKV hevc Main 10 10-bit DOVI 2160p 23.976 | Remux | Transcode | Remux | pass | SP-AV pass, TTFF 3.5 s |  |
| ATV-V08 | MKV hevc Main 10 10-bit DOVIWithEL 2160p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | **SP-AV fail** no video frame within 30 s (status 2): Cannot Decode |  |
| ATV-V08 | MKV hevc Main 10 10-bit DOVIWithEL 2160p 24.000 | Remux | DirectPlay | Remux | pass | **SP-AV fail** no video frame within 30 s (status 2): Cannot Decode |  |
| ATV-V09 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.8 s |  |
| ATV-V09 | MKV hevc Main 10 10-bit DOVIWithHDR10 1080p 24.000 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.0 s |  |
| ATV-V10 | MKV av1 Main 10-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pass | **SP-AV fail** errorLog: -12889 CoreMediaErrorDomain No response for media file in 3.003s |  |
| ATV-V10 | MKV av1 Main 10-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pass | pending |  |
| ATV-V11 | MKV vc1 Advanced 8-bit SDR 1080p 23.976 | Transcode, a:tc | DirectPlay | Transcode, a:tc | pass | SP-AV pass, TTFF 1.5 s |  |
| ATV-V11 | MKV vc1 Advanced 8-bit SDR 1080p 23.976 | Transcode, a:tc | DirectPlay | Transcode, a:tc | pass | pending |  |
| ATV-V12 | MKV mpeg2video Main 8-bit SDR 1080p 23.976 | Transcode | DirectPlay | Transcode | pass | SP-AV pass, TTFF 1.8 s |  |
| ATV-V12 | MKV mpeg2video Main 8-bit SDR 1080p 23.976 | Transcode, a:tc | DirectPlay | Transcode, a:tc | pass | pending |  |
| ATV-V13 | MKV h264 High 10 10-bit SDR 1080p 23.976 | Transcode | Transcode | Transcode | pass | **SP-AV fail** errorLog: -12889 CoreMediaErrorDomain No response for media file in 3.003s |  |
| ATV-V13 | MKV h264 High 10 10-bit SDR 1080p 23.976 | Transcode | Transcode | Transcode | pass | pending |  |
| ATV-V14 | TS h264 Main 8-bit SDR 720p 29.970 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.0 s |  |
| ATV-V14 | TS h264 High 8-bit SDR 1080p 29.970 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-V15 | AVI mpeg4 Advanced Simple Profile 8-bit SDR 320p 23.976 | Transcode, a:tc | DirectPlay | Transcode, a:tc | pass | SP-AV pass, TTFF 1.0 s |  |
| ATV-V15 | AVI mpeg4 Simple Profile 8-bit SDR 360p 23.976 | Transcode, a:tc | DirectPlay | Transcode, a:tc | pass | pending |  |
| ATV-V16 | MKV hevc Rext 10-bit SDR 540p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pass | **SP-AV fail** 1 timeline jumps while playing |  |
| ATV-V17 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.2 s |  |
| ATV-V17 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.1 s |  |
| ATV-V18 | MKV h264 High 8-bit SDR 404p 25.000 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.8 s |  |
| ATV-V18 | MKV h264 High 8-bit SDR 404p 25.000 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-V19 | MKV h264 High 8-bit SDR 1080p 24.000 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | SP-AV pass, TTFF 1.3 s |  |
| ATV-V19 | MKV hevc Main 10 10-bit DOVIWithHDR10 1080p 24.000 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-V20 | no item | | | | | |  |
| ATV-V21 | MKV h264 High 8-bit SDR 404p 59.940 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.0 s |  |
| ATV-V21 | MOV h264 High 8-bit SDR 1080p 59.940 | DirectPlay | DirectPlay | DirectPlay | pass | pending |  |
| ATV-V22 | MOV hevc Main 8-bit SDR 1072p 47.952 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.8 s |  |
| ATV-V22 | TS vc1 Advanced 8-bit SDR 1080p 47.952 | Transcode | DirectPlay | Transcode | pass | **SP-AV fail** errorLog: -12318 CoreMediaErrorDomain Segment exceeds specified bandwidth for variant |  |
| ATV-V24 | MKV h264 High 8-bit SDR 476p 29.970 | Transcode | Transcode | Transcode | pass | SP-AV pass, TTFF 0.9 s |  |
| ATV-V24 | MKV h264 High 8-bit SDR 432p 29.970 | Transcode, a:tc | Transcode | Transcode, a:tc | pass | pending |  |
| ATV-V23 | MKV h264 High 8-bit SDR 1080p 25.000 | Transcode, a:tc | DirectPlay | Transcode, a:tc | pass | **SP-AV fail** errorLog: -12318 CoreMediaErrorDomain Segment exceeds specified bandwidth for variant |  TODO on TV: #523 bandwidth; stalls or quality drops in play and after a seek. |
| ATV-V23 | MKV h264 High 8-bit SDR 1076p 23.976 | Transcode | DirectPlay | Transcode | pass | pending |  |
| ATV-A01 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | SP-AV pass, TTFF 1.8 s |  |
| ATV-A01 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | SP-AV pass, TTFF 1.7 s |  |
| ATV-A02 | MKV h264 High 8-bit SDR 1080p 24.000 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | SP-AV pass, TTFF 1.5 s |  |
| ATV-A02 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | pending |  |
| ATV-A03 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc | Remux, a:tc | Remux, a:tc | pass | **SP-AV fail** errorLog: -12889 CoreMediaErrorDomain No response for media file in 6.59s |  |
| ATV-A03 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc | Remux, a:tc | Remux, a:tc | pass | pending |  |
| ATV-A04 | MKV h264 High 8-bit SDR 1080p 24.000 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.0 s |  |
| ATV-A04 | MKV hevc Main 10 10-bit DOVIWithHDR10 1608p 23.976 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-A05 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.1 s |  |
| ATV-A05 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-A06 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | SP-AV pass, TTFF 1.3 s |  |
| ATV-A06 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | pending |  |
| ATV-A07 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | SP-AV pass, TTFF 1.4 s |  |
| ATV-A07 | MKV hevc Main 8-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | pending |  |
| ATV-A08 | MKV h264 High 8-bit SDR 368p 29.970 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | SP-AV pass, TTFF 0.9 s |  |
| ATV-A08 | MKV h264 High 8-bit SDR 600p 29.970 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | pending |  |
| ATV-A09 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux | DirectPlay | Remux | pass | **SP-AV fail** waited 2.7 s for data outside seeks |  |
| ATV-A10 | MOV h264 High 8-bit SDR 1080p 23.976 | DirectPlay | DirectPlay | DirectPlay | pass | SP-AV pass, TTFF 0.2 s |  |
| ATV-A10 | MOV h264 High 8-bit SDR 544p 23.976 | DirectPlay | DirectPlay | DirectPlay | pass | pending |  |
| ATV-A11 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.9 s |  |
| ATV-A11 | MKV h264 High 8-bit SDR 720p 23.976 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-A12 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.8 s |  |
| ATV-A12 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-A13 | MKV h264 High 8-bit SDR 804p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.9 s |  |
| ATV-A13 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.9 s |  |
| ATV-A14 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pass | SP-AV pass, TTFF 1.3 s |  |
| ATV-S01 | MOV h264 High 8-bit SDR 1040p 24.000 | DirectPlay, s:External | DirectPlay, s:External | Remux, s:Hls | pass | SP-AV pass, TTFF 0.1 s; **SF-AV fail** errorLog: -12642 CoreMediaErrorDomain #EXT-X-MEDIA: duplicate name (Undefined - SUBRIP - E… |  |
| ATV-S02 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pass | SP-AV pass, TTFF 0.9 s; SF-AV pass, TTFF 0.8 s |  |
| ATV-S02 | MKV h264 Main 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pass | pending |  |
| ATV-S03 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pass | SP-AV pass, TTFF 1.4 s; SF-AV pass, TTFF 2.3 s |  |
| ATV-S03 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pass | **SF-AV fail** errorLog: -12889 CoreMediaErrorDomain No response for media file in 3.003s |  |
| ATV-S04 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pass | SP-AV pass, TTFF 0.9 s; **SF-AV fail** 2 timeline jumps while playing |  TODO on TV: #522 on stock Swiftfin; recovers after a seek, no stall in play. |
| ATV-S04 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pass | pending |  |
| ATV-S05 | MKV h264 High 8-bit SDR 1080p 23.976 | Transcode, s:Encode | DirectPlay, s:Embed | Transcode, s:Encode | pass | SP-AV pass, TTFF 1.8 s |  |
| ATV-S05 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Transcode, s:Encode | DirectPlay, s:Embed | Transcode, s:Encode | pass | pending |  |
| ATV-S06 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pass | SP-AV pass, TTFF 1.3 s; SF-AV pass, TTFF 2.6 s |  |
| ATV-S06 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pass | **SF-AV fail** errorLog: -12889 CoreMediaErrorDomain No response for media file in 3.003s |  |
| ATV-S07 | MOV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pass | SP-AV pass, TTFF 0.8 s; SF-AV pass, TTFF 1.9 s |  |
| ATV-S07 | MOV hevc Main 8-bit SDR 1072p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pass | pending |  |
| ATV-S08 | MKV h264 High 8-bit SDR 1080p 29.970 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.8 s |  |
| ATV-S08 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-O01 | MKV h264 High 8-bit SDR 1080p 25.000 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.9 s |  |
| ATV-O01 | MKV h264 High 8-bit SDR 396p 29.970 | Remux | DirectPlay | Remux | pass | pending |  |
| ATV-O02 | MKV av1 Main 10-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pass | **SP-AV fail** 1 timeline jumps while playing |  |
| ATV-O02 | MKV av1 Main 10-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pass | pending |  |
| ATV-O03 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.0 s |  |
| ATV-O03 | MKV hevc Main 10 10-bit HDR10 2064p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.4 s |  |
| ATV-O04 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 1.7 s |  |
| ATV-O04 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Remux | DirectPlay | Remux | pass | SP-AV pass, TTFF 0.9 s |  |
| ATV-I01 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I01 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I02 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I02 | MKV hevc Main 10 10-bit SDR 800p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I03 | MKV hevc Main 10 10-bit DOVIWithHDR10 1608p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I03 | MKV hevc Main 10 10-bit DOVIWithHDR10 1608p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I04 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I04 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I05 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I05 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I06 | MKV hevc Main 10 10-bit SDR 800p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I06 | MKV hevc Main 10 10-bit DOVIWithHDR10 1080p 24.000 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I07 | MKV hevc Main 10 10-bit SDR 2160p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I07 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I08 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I08 | MKV hevc Main 10 10-bit HDR10 2064p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I09 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I09 | MKV hevc Main 10 10-bit DOVIWithHDR10 1608p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I10 | MKV hevc Main 10 10-bit SDR 800p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I10 | MKV hevc Main 10 10-bit DOVIWithHDR10 1080p 24.000 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I11 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I11 | MKV hevc Main 10 10-bit HDR10 2064p 23.976 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I12 | MOV hevc Main 8-bit SDR 1072p 47.952 | Remux | DirectPlay | Remux | pending | pending |  |
| ATV-I13 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pending | pending |  |
| ATV-I13 | MKV hevc Main 10 10-bit DOVIWithHDR10 1608p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pending | pending |  |
| ATV-I14 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pending | pending |  |
| ATV-I14 | MKV hevc Main 10 10-bit DOVIWithHDR10 1608p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pending | pending |  |
| ATV-I15 | MKV hevc Main 10 10-bit DOVIWithHDR10 1600p 23.976 | Remux, a:tc | Remux, a:tc | Remux, a:tc | pending | pending |  |
| ATV-I15 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux, a:tc | Remux, a:tc | Remux, a:tc | pending | pending |  |
| ATV-I16 | MKV hevc Main 10 10-bit DOVIWithHDR10 2076p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I16 | MKV hevc Main 10 10-bit HDR10 1600p 23.810 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I17 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I17 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I18 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I18 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I19 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I19 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I20 | MKV hevc Main 10 10-bit DOVIWithHDR10Plus 2074p 23.976 | Remux, a:tc | Remux, a:tc | Remux, a:tc | pending | pending |  |
| ATV-I20 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | Remux, a:tc | Remux, a:tc | pending | pending |  |
| ATV-I21 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I21 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I22 | MKV hevc Main 10 10-bit DOVIWithHDR10 1632p 23.976 | Remux, a:tc, s:External | DirectPlay, s:Embed | Remux, a:tc, s:Hls | pending | pending |  |
| ATV-I22 | MKV hevc Main 10 10-bit DOVIWithEL 2160p 23.976 | Remux, a:tc, s:External | DirectPlay, s:Embed | Remux, a:tc, s:Hls | pending | pending |  |
| ATV-I23 | MKV h264 High 10 10-bit SDR 1080p 23.976 | Transcode | Transcode | Transcode | pending | pending |  |
| ATV-I23 | MKV h264 High 10 10-bit SDR 1080p 23.976 | Transcode | Transcode | Transcode | pending | pending |  |
| ATV-I24 | MKV vc1 Advanced 8-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode, a:tc | Transcode, a:tc | pending | pending |  |
| ATV-I24 | MKV vc1 Advanced 8-bit SDR 1080p 23.976 | Transcode, a:tc | DirectPlay | Transcode, a:tc | pending | pending |  |
| ATV-I25 | MKV h264 High 8-bit SDR 1080p 25.000 | Transcode, a:tc | DirectPlay | Transcode, a:tc | pending | pending |  |
| ATV-I25 | MKV h264 High 8-bit SDR 1076p 23.976 | Transcode | DirectPlay | Transcode | pending | pending |  |
| ATV-I26 | MKV av1 Main 10-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pending | pending |  |
| ATV-I26 | MKV av1 Main 10-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pending | pending |  |
| ATV-I27 | MKV av1 Main 10-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pending | pending |  |
| ATV-I27 | MKV av1 Main 10-bit SDR 1080p 23.976 | Transcode, a:tc | Transcode | Transcode, a:tc | pending | pending |  |
| ATV-I28 | MKV hevc Main 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pending | pending |  |
| ATV-I28 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pending | pending |  |
| ATV-I29 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pending | pending |  |
| ATV-I29 | MKV hevc Main 10 10-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Transcode, s:Encode | pending | pending |  |
| ATV-I30 | MKV h264 High 10 10-bit SDR 1080p 23.976 | Transcode, s:External | Transcode, s:External | Transcode, s:Encode | pending | pending |  |
| ATV-I30 | MKV h264 High 10 10-bit SDR 1080p 23.976 | Transcode, s:External | Transcode, s:External | Transcode, s:Encode | pending | pending |  |
| ATV-I31 | MKV h264 High 10 10-bit SDR 1080p 23.976 | Transcode | Transcode | Transcode | pending | pending |  |
| ATV-I31 | MKV h264 High 10 10-bit SDR 1080p 23.976 | Transcode | Transcode | Transcode | pending | pending |  |
| ATV-I32 | MKV hevc Main 10 10-bit DOVI 2160p 23.976 | Remux | Transcode | Remux | pending | pending |  |
| ATV-I32 | MKV hevc Main 10 10-bit DOVI 2160p 23.976 | Remux | Transcode | Remux | pending | pending |  |
| ATV-I33 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux, a:tc, s:External | Remux, a:tc, s:Encode | Transcode, a:tc, s:Encode | pending | pending |  |
| ATV-I33 | MKV hevc Main 10 10-bit HDR10 2160p 23.976 | Remux, a:tc, s:External | Remux, a:tc, s:Encode | Transcode, a:tc, s:Encode | pending | pending |  |
| ATV-I34 | MKV hevc Main 10 10-bit DOVI 2160p 23.976 | Remux | Transcode | Remux | pending | pending |  |
| ATV-I34 | MKV hevc Main 10 10-bit DOVI 2160p 23.976 | Remux | Transcode | Remux | pending | pending |  |
| ATV-I35 | MKV hevc Main 10 10-bit DOVI 2160p 23.976 | Remux | Transcode | Remux | pending | pending |  |
| ATV-I35 | MKV hevc Main 10 10-bit DOVI 2160p 23.976 | Remux | Transcode | Remux | pending | pending |  |
| ATV-I36 | MKV hevc Main 10 10-bit DOVIWithEL 2160p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I36 | MKV hevc Main 10 10-bit DOVIWithEL 2160p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I37 | MOV h264 High 8-bit SDR 404p 23.976 | DirectPlay | DirectPlay | DirectPlay | pending | pending |  |
| ATV-I37 | MOV h264 High 8-bit SDR 1080p 25.000 | DirectPlay | DirectPlay | DirectPlay | pending | pending |  |
| ATV-I38 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, s:External | DirectPlay, s:Embed | Remux, s:Hls | pending | pending |  |
| ATV-I38 | MKV h264 High 8-bit SDR 1080p 23.976 | Remux, a:tc, s:External | DirectPlay, s:Embed | Remux, a:tc, s:Hls | pending | pending |  |
| ATV-I39 | MOV h264 High 8-bit SDR 818p 23.976 | DirectPlay | DirectPlay | DirectPlay | pending | pending |  |
| ATV-I39 | MOV h264 High 8-bit SDR 816p 23.976 | DirectPlay | DirectPlay | DirectPlay | pending | pending |  |
| ATV-I40 | MKV hevc Main 10 10-bit DOVIWithHDR10 1632p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |
| ATV-I40 | MKV hevc Main 10 10-bit DOVIWithHDR10 2076p 23.976 | Remux, a:tc | DirectPlay | Remux, a:tc | pending | pending |  |

<!-- /qamatrix:results -->

## Cells that need a human eye

No probe replaces these checks: HDR and Dolby Vision mode switching, Atmos
passthrough at the receiver, subtitle styling, and channel mapping.

<!-- qamatrix:human -->

- **ATV-R03** MKV HEVC Main10 SDR 4K scope EAC3 5.1, resumed at 2:58: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-V05** MKV HEVC HDR10 4K TrueHD Atmos 7.1: HDR mode switch; audio is a PCM/AAC/AC3 transcode, not Atmos.
- **ATV-V06** MKV HEVC HDR10+ 4K: HDR mode on the TV: HDR10+ (or HDR10 on TVs without HDR10+).
- **ATV-V07** Dolby Vision profile 5 (DOVI, no HDR10 fallback): Dolby Vision mode switch and colors.
- **ATV-V08** Dolby Vision profile 7 (DOVIWithEL): HDR/DV mode and colors.
- **ATV-V09** Dolby Vision profile 8.1 (DOVIWithHDR10): Dolby Vision mode switch.
- **ATV-V24** Anamorphic SD H.264 (DVD rip): Aspect ratio.
- **ATV-V23** Interlaced H.264: Bandwidth: the mapped item declares 28.57 Mbit/s in the master playlist, but segments peak at 38.1 Mbit/s, so macOS AVPlayer logs -12318 (upstream parity). On the TV, watch for stalls or quality drops during play and after a seek.
- **ATV-A02** DTS-HD MA 7.1: Channel mapping (rear vs side surrounds).
- **ATV-A03** TrueHD Atmos below 4K: Receiver shows multichannel PCM/AC3, not Atmos (expected).
- **ATV-A04** EAC3 Atmos (DD+ JOC): Receiver shows Dolby Atmos.
- **ATV-A07** Opus 5.1 in MKV: Channel mapping.
- **ATV-A09** AC3 mono: Mono plays centred.
- **ATV-S02** Embedded forced SRT: Forced subtitles appear without selecting a track.
- **ATV-S03** ASS with embedded fonts (styled anime): Subtitle styling (fonts, karaoke, signs).
- **ATV-S04** PGS (Blu-ray image subtitles): PGS position and size on 4K. On stock Swiftfin (an iGPU limit): the seek segment takes 6.3-6.7 s and the transcode runs at about 1.5x realtime. Confirm that play recovers after a seek and does not stall.
- **ATV-O01** Next-episode autoplay across a remux: Autoplay countdown and resume.
- **ATV-O02** Background and foreground during a transcode: Background for 2 min, then return.
- **ATV-O03** HDR/SDR mode switch between items: TV mode switch both ways.
- **ATV-O04** Scrub with trickplay on a 4K remux: Scrub feel and thumbnails.
- **ATV-I01** Remux a:copy, start at 0: 4K SDR 10-bit + EAC3 5.1 (control for R03): Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Start: on the item page choose Play (or Play from beginning when it offers Resume). Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I02** Remux a:copy, resume off the 6 s grid: 1080p HEVC + EAC3/AC3: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I03** Remux a:copy, resume: Dolby Vision 8.1 + EAC3 Atmos: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. DV and Atmos indicators after the resume.
- **ATV-I04** Remux a:copy, seek forward far: 4K HDR, runtime over 150 min: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub to 90:00. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I05** Remux a:copy, seek backward behind the job: 4K HEVC: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub forward to 20:00, play 1 min, then scrub back to 5:00. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I06** Remux a:copy, seek to the last 30 s: MKV HEVC episode: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub to the last 30 s. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I07** Remux a:copy, audio-track switch mid-play: two AAC/AC3/EAC3 tracks: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Audio switch: at about 5:00, click the touch surface, swipe up to the action buttons, choose Audio and pick the other track. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I08** Remux a:copy, pause over 5 min then resume: 4K: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Pause: press Play/Pause at about 3:00, wait 6 min (if the screen saver starts, press Play/Pause once to wake it), then press Play/Pause to play. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Pause for over 5 min.
- **ATV-I09** Remux a:copy, background and foreground: Dolby Vision 8.1: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Background: press the TV button for the Home screen, wait 2 min, then open the app again from Home. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Home button for 2 min, then return.
- **ATV-I10** Remux a:copy, next-episode autoplay: HEVC + EAC3 series: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Next episode: scrub to the last 60 s and let the episode end without pressing anything; the next one must start by itself. Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Autoplay to the next episode.
- **ATV-I11** Two concurrent 4K remux streams (two devices): Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Second stream: start this item on the Apple TV, then the second item on another client (phone app or web). Check the path: the expected delivery is Remux a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Needs a second client.
- **ATV-I12** Remux a:copy, resume: variable frame rate: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Remux. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I13** Remux a:copy + PGS on, resume: 4K: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Remux a:copy s:External. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Subtitle timing after a resume.
- **ATV-I14** Remux a:copy, subtitle switch mid-play: text to image to off: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Subtitles: click the touch surface, swipe up to the action buttons, choose Subtitles; switch tracks and turn them off as the pass condition says. Check the path: the expected delivery is Remux a:copy s:External. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Remux. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Subtitle menu switches.
- **ATV-I15** Remux a:transcode, resume: 4K HDR + TrueHD Atmos: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Remux a:transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Stream. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I16** Remux a:transcode, seek forward far: DTS-HD MA, long runtime: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub to 80:00. Check the path: the expected delivery is Remux a:transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Stream. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I17** Remux a:transcode, seek backward: DTS 5.1: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub forward to 15:00, play 1 min, then scrub back to 5:00. Check the path: the expected delivery is Remux a:transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Stream. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I18** Audio switch from a copy track to a transcode track: EAC3/AC3 to TrueHD/DTS: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Audio switch: at about 5:00, click the touch surface, swipe up to the action buttons, choose Audio and pick the other track. Check the path: the expected delivery is Remux a:transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Stream. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I19** Remux a:transcode, pause over 5 min and background: Opus or DTS: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Pause: press Play/Pause at about 3:00, wait 6 min (if the screen saver starts, press Play/Pause once to wake it), then press Play/Pause to play. Background: press the TV button for the Home screen, wait 2 min, then open the app again from Home. Check the path: the expected delivery is Remux a:transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Stream. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Pause and background.
- **ATV-I20** Remux a:transcode, seek to the last 30 s: TrueHD: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub to the last 30 s. Check the path: the expected delivery is Remux a:transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Stream. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I21** Remux a:transcode, next-episode autoplay: Opus or FLAC series: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Next episode: scrub to the last 60 s and let the episode end without pressing anything; the next one must start by itself. Check the path: the expected delivery is Remux a:transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Stream. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Autoplay to the next episode.
- **ATV-I22** Text subtitle switch on stock Swiftfin: HLS WebVTT, DTS: Setup: stock Swiftfin, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Subtitles: click the touch surface, swipe up to the action buttons, choose Subtitles; switch tracks and turn them off as the pass condition says. Check the path: the expected delivery is Remux a:transcode s:Hls. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Stream. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Stock Swiftfin build.
- **ATV-I23** Transcode, resume: H.264 Hi10P anime: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I24** Transcode, seek forward far then to the end: AV1, VC-1 or MPEG-2: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub to 40:00, play 1 min, then scrub to the last 30 s. Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I25** Transcode, seek backward: interlaced (deinterlace): Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub forward to 15:00, play 1 min, then scrub back to 5:00. Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I26** Transcode, pause over 5 min then resume: AV1: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Pause: press Play/Pause at about 3:00, wait 6 min (if the screen saver starts, press Play/Pause once to wake it), then press Play/Pause to play. Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Pause for over 5 min.
- **ATV-I27** Two concurrent streams: a video transcode and a 4K audio-transcode remux: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Second stream: start this item on the Apple TV, then the second item on another client (phone app or web). Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Needs a second client.
- **ATV-I28** Burn-in toggle on stock Swiftfin: PGS on and off mid-play: Setup: stock Swiftfin, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Subtitles: click the touch surface, swipe up to the action buttons, choose Subtitles; switch tracks and turn them off as the pass condition says. Check the path: the expected delivery is Transcode s:Encode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Stock Swiftfin build.
- **ATV-I29** Burn-in, resume on stock Swiftfin: ASS with fonts: Setup: stock Swiftfin, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Transcode s:Encode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Stock Swiftfin build; cue timing.
- **ATV-I30** Transcode, next-episode autoplay: Hi10P anime with ASS: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Next episode: scrub to the last 60 s and let the episode end without pressing anything; the next one must start by itself. Check the path: the expected delivery is Transcode s:External. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Autoplay and ASS on the next episode.
- **ATV-I31** Transcode, dual audio switch: Hi10P anime Japanese to English: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Audio switch: at about 5:00, click the touch surface, swipe up to the action buttons, choose Audio and pick the other track. Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I32** Tone map, resume: Dolby Vision 5 on SharkParty VLC: Setup: SharkParty, Settings > Video player: VLC. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Make a resume point: play from the beginning and press Back at about 2:58. On a film, play past 6% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved. Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Colors after the resume.
- **ATV-I33** Tone map, seek forward and back: 4K HDR10 + PGS burn-in on stock Swiftfin: Setup: stock Swiftfin, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub forward to 40:00, play 1 min, then scrub back to 10:00. Check the path: the expected delivery is Transcode s:Encode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Stock Swiftfin build; colors and stalls.
- **ATV-I34** Tone map, pause and background: Dolby Vision 5 on SharkParty VLC: Setup: SharkParty, Settings > Video player: VLC. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Pause: press Play/Pause at about 3:00, wait 6 min (if the screen saver starts, press Play/Pause once to wake it), then press Play/Pause to play. Background: press the TV button for the Home screen, wait 2 min, then open the app again from Home. Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Pause and background.
- **ATV-I35** Two concurrent streams: a 4K tone map and a 4K remux: Setup: SharkParty, Settings > Video player: VLC. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Second stream: start this item on the Apple TV, then the second item on another client (phone app or web). Check the path: the expected delivery is Transcode. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Transcode. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Needs a second client.
- **ATV-I36** DirectPlay, resume and seek far on SharkParty VLC: 4K DV profile 7 + DTS-HD MA: Setup: SharkParty, Settings > Video player: VLC. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Resume is blocked on rc.8: the server saves no position for direct play. Until the fix lands, start from the beginning and scrub to the point instead. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub forward to +30 min, play 1 min, then scrub back to -15 min. Check the path: the expected delivery is DirectPlay a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Play. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I37** DirectPlay, seek near end and next episode: MP4 H.264 episode: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Seek: play 1 min, then click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s. Scrub to the last 30 s. Next episode: scrub to the last minute and let the episode end without pressing anything; the next one must start by itself. Check the path: the expected delivery is DirectPlay a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Play. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Autoplay.
- **ATV-I38** DirectPlay, audio and subtitle switch on SharkParty VLC: DTS + a second track, PGS + SRT: Setup: SharkParty, Settings > Video player: VLC. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Audio switch: at about 5:00, click the touch surface, swipe up to the action buttons, choose Audio and pick the other track. Subtitles: click the touch surface, swipe up to the action buttons, choose Subtitles; switch tracks and turn them off as the pass condition says. Check the path: the expected delivery is DirectPlay a:copy s:Embed. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Play. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.
- **ATV-I39** DirectPlay, pause over 5 min and background: MP4 H.264 movie: Setup: SharkParty, Settings > Video player: Native. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Pause: press Play/Pause at about 3:00, wait 6 min (if the screen saver starts, press Play/Pause once to wake it), then press Play/Pause to play. Background: press the TV button for the Home screen, wait 2 min, then open the app again from Home. Check the path: the expected delivery is DirectPlay a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Play. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Pause and background.
- **ATV-I40** Two concurrent 4K direct plays on SharkParty VLC: Setup: SharkParty, Settings > Video player: VLC. Settings > Video player > Playback quality > Device profile > Compatibility must be Auto. Second stream: start this item on the Apple TV, then the second item on another client (phone app or web). Check the path: the expected delivery is DirectPlay a:copy. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read Direct Play. If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result. Needs a second client.

<!-- /qamatrix:human -->

Also check these by hand in any cell, because the probe cannot:

- next-episode autoplay and the autoplay countdown (ATV-O01);
- background and foreground during a transcode (ATV-O02);
- scrubbing feel and trickplay thumbnails (ATV-O04);
- resume from position after the app restarts;
- the subtitle switch and subtitle off during playback (ATV-S01 to ATV-S08).

## Device automation

The operator check runs by hand. A driver for a real Apple TV can automate
most of it:

- **Remote control:** [pyatv](https://pyatv.dev) pairs over the LAN. It
  launches an app by bundle ID, sends remote keys, and reads the now-playing
  title, position, state and duration.
- **Server evidence:** the server logs and session API give the decision,
  the fetched ranges, the progress reports, and the resume and played-state
  round trip. `qamatrix check` reads these already.
- **Screenshots:** a developer-mode pairing (`pymobiledevice3` or Xcode
  Devices) takes screenshots for subtitle and HDR checks.

A driven cell opens the item, plays it, waits for the playing state,
compares the server decision, seeks and confirms that the position and the
progress reports move, takes a screenshot, stops, and checks resume and
played state. A/V sync, audio dropouts and HDR tone mapping still need a
person.

Setup needs Developer Mode on the Apple TV and pairing PINs confirmed on the
TV. The same driver covers SharkParty and Infuse. Infuse ships only through
the App Store, so it does not run in the tvOS Simulator; Infuse for Mac
gives the closest desktop check.

## Notes

- **HEVC in MP4 never direct-plays on AVPlayer (ATV-V03).** The AVPlayer
  profile requires the codec tag `hvc1` or `dvh1`. Pinned upstream never
  fills `MediaStream.CodecTag`, because `MediaStreamInfo.cs:204` binds the
  literal key `codec_tag_string?`. Deadfin matches it
  (`internal/probe/normalize_test.go`). So HEVC MP4 remuxes on stock
  Jellyfin as well. This is parity, not a defect.
- **ASS on stock Swiftfin is burned in.** `MediaStream.SupportsSubtitleConversionTo`
  refuses ASS and SSA, so there is no WebVTT path. This is upstream behavior.
- **VC-1 with TrueHD on VLC transcodes.** HLS cannot carry VC-1
  (`StreamBuilder.cs:31`), so an audio-only reason forces a video transcode.
- **Remux playlist drift is parity (ATV-V03, V14, V22, S01, S07).** Upstream
  builds keyframe-based segments only for a video remux whose file passes
  `AllowOnDemandMetadataBasedKeyframeExtractionForExtensions` (`mkv`) and has
  Matroska Cues (`DynamicHlsPlaylistGenerator.cs:37-46`). Other remuxes get
  equal-length segments, and ffmpeg stream copy cuts only at source
  keyframes, so segment media can sit seconds from its playlist time. The
  media timeline stays continuous and the probe seeks land on target. MKV
  remuxes that drift (ATV-A01, ATV-A13) need Cues-based segments.
- **Duplicate subtitle rendition names (ATV-S01 on stock Swiftfin).** The
  master playlist names each subtitle rendition by `MediaStream.DisplayTitle`,
  as upstream does (`DynamicHlsHelper.cs:682-686`). Two sidecar SRT files with
  no language both read `Undefined - SUBRIP - External`, and AVPlayer logs
  -12642 `duplicate name ... for rendition group "subs"`. Playback continues.
  Upstream does the same; the operator checks the subtitle menu on the TV.
- **QSV transcode GOP (ATV-V22).** h264_qsv cannot force keyframes, so
  the GOP is `SegmentLength * RealFrameRate` of the source. When `MaxFramerate`
  caps the output below the source rate, segments are longer than the
  playlist says. Upstream uses the same formula.
- **Large MKV first playlist (ATV-V08, ATV-A09).** On v0.2.32,
  `main.m3u8` waited for a full keyframe scan and timed out at 3 minutes.
  v0.2.33 reads Matroska Cues: the first segment comes in under 1 s
  and MKV remux playlists follow the source keyframes.
- **Dolby Vision profile 7 (ATV-V08).** Both AVPlayer profiles declare `DOVI`
  and `DOVIWithEL`, so the server copies the profile 7 stream with the `dvh1`
  tag, as upstream does (`DynamicHlsController.cs:1803-1819`). macOS AVPlayer
  reports "Cannot Decode". The client profiles should not declare
  `DOVIWithEL` for AVPlayer; then the server strips the RPU and sends the
  HDR10 base layer (`EncodingHelper.cs:1437`). Operator: confirm on the TV.
- **One play session per run.** `qamatrix check` and `qamatrix plan`
  post PlaybackInfo again for each stream, so every run has its own
  PlaySessionId. Before this, two runs of one matrix file reused the mapped
  PlaySessionId: they were one session with two clients, and each run's stop
  ended the playlist family under the other. Upstream keys a job the same
  way (`StreamingHelpers.GetOutputFilePath`), so this was a tool defect, not
  a server defect.
- **Transcode seek latency.** After a +300 s seek, a 1080p QSV transcode
  needs 6-8 s for its first segment. AVPlayer times out once at 3 s
  (-12889, then -15628) and recovers; there are no stalls during play. The
  SD transcode (ATV-V24) seeks in 0.2 s. ASS burn-in on stock Swiftfin
  (ATV-S03, ATV-S06) can fail to start: the init segment times out twice and
  AVPlayer gives up.
- **Pod CPU limit removed.** After the change, seeks on
  software-decoded sources (AV1, ATV-V10 and ATV-O02; HEVC Rext, ATV-V16)
  show a frame 1.2-3.2 s after the seek. The v0.2.35 table above shows the
  results from before the change.
