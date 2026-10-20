# Federated libraries

Status: proposal, open for discussion.

## Goal

Separate Deadfin deployments form a **pool**. Each deployment is a
**site**. A site can be one node or many.

- Users of one site browse and play the libraries that other sites share
  with it, from their usual client, with their usual account.
- A title that exists at two or more sites stays playable when one site
  goes down.
- Each site admin keeps full control of the site: its users, its
  libraries, and which libraries each peer site can see.

Clients see ordinary Jellyfin libraries and items. A client needs no
change.

## Prior work

- **Upstream proposal.** [jellyfin-meta#118][meta118] and the draft core
  change [jellyfin#16494][pr16494] add *external items* to Jellyfin:
  - A library scan calls `IExternalItemProvider` implementations. The
    scan upserts the items they return as `SourceType.External` and
    removes items that the provider no longer returns.
    `IsAvailableAsync` lets an offline provider skip the sync and keep
    its items.
  - A stream request for an external item (`/Videos/{id}/stream`,
    `/Audio/...`, HLS) returns `302` to the remote server before the
    transcode pipeline starts.
  - The redirect URL carries a `StreamToken`:
    `base64url("{itemId}|{expiresUnixSeconds}").hex(HMAC-SHA256)`. The
    remote server validates it. The client needs no remote account.
  - The client must reach the remote server directly.
- **[Jellyswarrm][swarrm].** A reverse proxy that presents several
  Jellyfin servers as one. It maps users across servers and merges
  catalogs live. Media streams from the origin server.
- **[JellyfinFederationPlugin][angablade].** A plugin that streams
  libraries between servers without copying files.

These designs share the catalog and route playback. They keep one copy of
each file, so a title is only available while its site is up.

## Design

The design has four layers. Each layer is useful without the layers
after it.

### 1. Trust: sites and pairing

- Each site has an Ed25519 key pair. The **site ID** is the SHA-256 of
  the public key, in base32. The site ID is stable for the life of the
  key.
- Two sites **pair** with an invite token from the admin panel:

  ```
  dfp1.<inviteId>.<secret>.<fingerprint>
  ```

  - `fingerprint` pins the TLS key of the inviting site, so a
    self-signed certificate works.
  - The two sites run a mutual HMAC challenge with the secret. The secret
    never crosses the wire.
  - The pairing mints a per-peer credential on each side. An admin can
    revoke a peer at any time.
  - An invite has an optional expiry and use count.
- A pool is the set of pairwise links. The pool has no central server
  and no consensus protocol. Two sites that are not paired do not see
  each other.
- An admin shares individual libraries with individual peers. The share
  list is the only access rule a peer sees.

### 2. Catalog federation

- Each shared library has one **owner** site at a time, and the owner is
  the only writer of the library's items. Each site keeps an append-only
  **change feed** of the libraries it owns, with a sequence number. Each
  entry names the library's ownership epoch (see
  [Ownership](#ownership-and-transfer)) and is signed with the site key.
- A peer pulls the feed from its last cursor. It pulls on a timer and
  when the origin sends a push hint over the peer connection. The first
  pull is a full snapshot.
- The peer stores remote items in its own store as external items, in
  the shape of the upstream proposal. Browse, search, filters, Latest,
  and Next Up run as local queries. A slow or offline peer does not slow
  the UI.
- Images are fetched from the origin on first request and cached.
- **Item identity.** A shared library gets a pool-wide `libraryId` when
  it is first shared. A remote item gets a deterministic local ID:
  `UUIDv5(libraryId, remoteItemId)`. The ID survives resyncs and
  ownership transfers, so client caches and playlists stay valid.
- **Users stay home.** A user signs in to the home site only. Watch
  state, resume points, favorites, and playlists stay on the home site.
  The home site sends a peer a short-lived signed claim,
  `{siteId, userId, parental rating, expiry}`. A peer applies its share
  list and the claimed parental rating, and never sees a password.

### 3. One title, many sources

- The home site merges items from different sites into one logical item
  when their provider IDs match (`Tmdb`, `Tvdb`, `Imdb`; for episodes,
  the series IDs plus season and episode number).
- Each site's copy becomes one entry in `MediaSources`. Jellyfin already
  models alternate versions this way, so clients show a version picker
  with no change.
- `PlaybackInfo` ranks the reachable sources:
  1. a local file;
  2. a peer source by redirect (`302` plus a stream token);
  3. a peer source through the home site (proxy).

  The home site tracks peer health. It leaves out a source on a peer
  that does not answer, so playback fails over to the next copy with no
  user action.
- The origin site makes the transcode decision. The home site forwards
  the client's device profile with the request.

### 4. Replication

Media files are written once and read many times. Replication therefore
needs no synchronous write path, and copies of a file never conflict.
The model follows Longhorn: a policy, scheduled replicas, a degraded
state, and rebuilds.

- **Policy per library**, set by the owning site, for example:

  ```yaml
  replicas: 2
  spread: site        # at most one replica per site
  peers: [all]        # or a list of site IDs
  window: "01:00-07:00"
  maxRateMbps: 50
  ```

- **Capacity offers.** A site offers a peer a storage path and a quota
  for replicas. A site never stores data it did not offer room for.
- **Owner schedules.** The site that owns a library compares the policy
  with the live replica count and asks peers with free capacity to pull
  copies.
- **Content addressing.**
  - A quick fingerprint (size plus BLAKE3 of sampled chunks) finds
    candidate matches.
  - Full block hashes (BLAKE3, fixed 4 MiB blocks) are computed when a
    file enters replication.
  - A file that a peer already holds becomes a replica with zero
    transfer.
- **Transfer.** Resumable and block-verified, inside the policy window
  and rate limit. A puller fetches blocks from every site that holds a
  copy, so a rebuild does not depend on one uplink.
- **Replica on disk.** A replica is a normal file under the replica
  path. The local site plays it as a local file, with local transcoding
  and direct play. Replica paths are kept apart from the site's own
  library paths.
- **States.**
  - *Healthy*: replica count matches the policy.
  - *Degraded*: fewer replicas than the policy. The owner schedules a
    rebuild.
  - *Orphaned*: the owner's lease has expired (below). Replicas keep
    serving. Ownership moves only by a transfer record.

### Ownership and transfer

A partition and a dead owner look the same from outside. Ownership
therefore moves only on proof that the owner authorized it, confirmed by
more than one site.

- **Epochs.** Each shared library carries `(libraryId, epoch, owner)`.
  Every feed entry names its epoch. A peer accepts entries only from the
  highest epoch it has seen for the library. The epoch is a fencing
  token: a former owner's writes stop counting once a higher epoch
  exists.
- **Lease.** The owner signs a lease into its feed on a fixed interval,
  for example every 15 minutes with a 24-hour expiry. Peers use the same
  leases for health tracking.
- **Succession grant.** While healthy, the owner signs a grant that names
  the heir sites and a minimum orphan time, for example 7 days. Without a
  grant, the library has no heirs.
- **Transfer record.** An heir starts a transfer to `epoch + 1` after the
  lease has expired for the minimum orphan time. The record is valid
  only with:
  1. the owner's succession grant that names the heir;
  2. signed acknowledgements from a majority of the library's replica
     sites. Each witness confirms that it also cannot reach the owner and
     that the newest lease it holds has expired.

  Peers verify the grant signature and the witness count. An heir with
  only a broken link of its own cannot collect a majority.
- **The former owner returns.** It reads the higher epoch, marks its copy
  of the library read-only, and becomes a replica. Items it added during
  the partition go to the new owner as proposals; files are
  content-addressed, so a proposal for a file the pool already holds
  costs no transfer. Ownership moves back only by a new transfer record
  that the current owner signs.
- **Fork.** An admin can always copy a library they hold replicas of
  into a new library with a new `libraryId`, owned by their site. Peers
  subscribe to the fork by choice. A fork leaves the original library and
  its epoch unchanged.

## Upstream compatibility

- Layers 1 to 3 use the upstream external-item contract: external items,
  `302` redirects, and `StreamToken`. A Deadfin site can then consume a
  stock Jellyfin server that runs the upstream remote-library plugin, and
  serve one.
- Layer 4, the pairing protocol, and multi-site source merging are
  Deadfin extensions between Deadfin sites. Each gets a row in
  `docs/divergences.md` when it lands.

## Phases

1. Pairing, one read-only shared library from one peer, redirect
   playback. Upstream-compatible stream tokens.
2. Change feed with push hints, deterministic IDs, user claims, several
   peers.
3. Merge by provider ID into multiple `MediaSources`, peer health, and
   failover. Proxy playback for clients that cannot reach a peer.
4. Replication policies, capacity offers, block transfer, rebuild.
5. Ownership leases, succession grants, witnessed transfer, fork.

## Open questions

1. **Redirects in clients.** Which clients follow a `302` to another
   host for direct play and for HLS, and keep the stream token? AVPlayer
   (Swiftfin, Infuse), ExoPlayer (Findroid, Android TV), and jellyfin-web
   need a test pass before phase 1 picks redirect or proxy as the
   default.
2. **Merge rules.** Is a provider-ID match strong enough? Editions
   (director's cut), 3D, and extras need a rule.
3. **Remote transcoding load.** Should a site cap the transcodes it runs
   for peers, separately from its own users?
4. **Bandwidth.** One 60 GB remux at 10 Mbit/s upload takes about 13
   hours. Are per-library policies plus sampled dedup enough, or does
   replication need a "transcoded replica" mode (for example, a 1080p
   HEVC copy of a 4K remux)?
5. **Discovery.** Is pairing by invite token enough, or does a pool want
   an optional shared directory of sites?
6. **Live TV and music.** Do they federate in phase 1, or later?
7. **Legal exposure.** Streaming to a peer and copying files to a peer
   are different acts. Replication is opt-in per library and per peer.
   Does a site also need a pool-wide switch to turn it off?
8. **Witnesses in small pools.** With `replicas: 2`, the heir is often
   the only replica site, so it would witness its own transfer. Should
   every site that subscribes to the library count as a witness, with a
   floor of two witnesses other than the heir?

[meta118]: https://github.com/jellyfin/jellyfin-meta/discussions/118
[pr16494]: https://github.com/jellyfin/jellyfin/pull/16494
[swarrm]: https://github.com/LLukas22/Jellyswarrm
[angablade]: https://github.com/Angablade/JellyfinFederationPlugin
