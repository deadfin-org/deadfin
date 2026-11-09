# Deadfin deployment and security guide

This page adds security and operations detail to the [install guide](install.md).
Examples use the placeholder `vX.Y.Z`. Select a published release and pin its
digest. The community image name is `ghcr.io/deadfin-org/deadfin`; an operator
testing an unpublished build must substitute its own registry and image digest.
Check [known differences](known-differences.md) and the release notes for the
version you install.

This guide covers a new installation. For migration of users and watch state,
follow [importing from Jellyfin](importing-from-jellyfin.md). Keep the original
Jellyfin data untouched until the new server is accepted.

## 1. Choose a deployment shape

| Shape | Use it when | Main constraint |
| --- | --- | --- |
| Docker Compose | One Linux host, NAS, or VM | Keep host mounts and backups under your control. |
| Rootless Podman | One Linux host where you want a rootless runtime | Check UID mapping, supplementary groups, and GPU access. |
| Kubernetes | You already operate a cluster and CSI storage | Use one writer for Deadfin state; verify your CNI enforces NetworkPolicy. |
| Native Linux service | You build and package Deadfin yourself | Supply libvips and FFmpeg; manage the process and sandbox yourself. |
| NAS app UI (Unraid, TrueNAS, etc.) | The UI can deploy the OCI image | Map the same paths and ports as the Compose example; inspect generated privileges. |

Deadfin is a Go server that speaks the Jellyfin 12.0.0 API. The image holds the
server and the deadfin-web client, which Deadfin serves at `/web/`. To use
`jellyfin-web` instead, mount its files at `/var/lib/deadfin/web`. You can also
deploy `deadfin-web` as its own service. The [known differences](known-differences.md) page lists the
Jellyfin features that Deadfin provides. Deadfin uses two ports: TCP 8096 for
HTTP and WebSocket, and UDP 7359 for Jellyfin client discovery. UDP 7359 is
optional when clients use a server URL. A bridge or pod network is sufficient;
Deadfin needs no host networking.

### Exposure decision

Start on a trusted LAN or behind a VPN. For remote access, terminate HTTPS at
a maintained reverse proxy and limit who can reach it. Do not publish TCP 8096
directly to the internet during setup. First-run access has a setup-link
mechanism, but some container networks can make remote clients appear local.

Anyone who can write files into a scanned media folder can put content in
front of your users, so give media write access only to people you trust. Run
the newest release tag: security fixes go there (see
[SECURITY.md](../../SECURITY.md)), and published advisories are at
<https://github.com/deadfin-org/deadfin/security/advisories>. Network isolation
limits what a compromised process can reach; application authorization is a
separate layer.

## 2. Storage, identity, and secrets

The image runs as UID/GID `65532:65532`. Give that identity write access to
`/var/lib/deadfin`. The data root holds config, the SQLite database at
`data/deadfin.db`, metadata, images, cache and transcodes, logs, and library
definitions. Back up the **whole** root. Keep it on local or block-backed
storage; do not put the SQLite database on NFS or SMB. Media may be on NFS/SMB.

Mount media read-only by default. The current alpha stores uploaded subtitles
and edited images in Deadfin's data root and does not write NFO edits beside
media. Keep media read-only unless a later feature explicitly needs writes.
A read-only media mount also limits what a server compromise can alter.
Avoid mounting the host root, Docker/Podman socket, Kubernetes credentials,
SSH keys, or unrelated application data into the server.

SQLite is suitable for one Deadfin process. PostgreSQL is supported, but its
URL contains credentials. Use a secret store or an operator-readable environment
file outside version control; restrict access to the container definition,
deployment API, and process environment. PostgreSQL needs a private database
and schema with the `CREATE` privilege. Use TLS to a remote database. Back up
both PostgreSQL and the Deadfin data root. With SQLite, run one replica. With
PostgreSQL, two or more replicas can run on one shared data root (D-121).
`docs/transcode-workers.md` §5 lists the settings and the current limits.

Optional variables include `TMDB_API_KEY`, `AUDIODB_API_KEY`,
`LISTENBRAINZ_TOKEN`, `LISTENBRAINZ_SERVER`, `DEADFIN_LOG_LEVEL`,
`DEADFIN_SQLITE_CACHE_MB`, and `DEADFIN_SQLITE_MMAP_MB`. Keep
`DEADFIN_PPROF_ADDR` unset in normal service. If profiling is needed, bind it
to loopback only and remove it afterward. Do not log or paste setup links,
API keys, database URLs, or tokens into issue reports.

## 3. Docker Compose reference

On a Linux host, prepare a data directory owned by the container identity.
The image includes deadfin-web; to use jellyfin-web instead, add the web mount
that the Deadfin install guide describes. Replace the
media path and image tag. This example binds HTTP to host loopback, assuming a
reverse proxy on the same host. To serve a trusted LAN directly, bind the host's
LAN address instead. Remove the UDP port if discovery is not needed.

```yaml
services:
  deadfin:
    image: ghcr.io/deadfin-org/deadfin:vX.Y.Z
    restart: unless-stopped
    init: true
    ports:
      - "127.0.0.1:8096:8096/tcp"
      # Optional LAN discovery: bind or firewall this to the trusted LAN.
      # - "7359:7359/udp"
    env_file:
      - path: ./deadfin.env
        required: false
    volumes:
      - ./deadfin/data:/var/lib/deadfin
      - /srv/media:/media:ro
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    pids_limit: 512
    mem_limit: 4g
    cpus: 4
    stop_grace_period: 60s
```

```sh
mkdir -p ./deadfin/data
sudo chown -R 65532:65532 ./deadfin/data
chmod 0700 ./deadfin/data
docker compose config
docker compose up -d
docker compose logs --tail=100 deadfin
curl --fail http://127.0.0.1:8096/System/Info/Public
```

`deadfin.env` may contain `TMDB_API_KEY=...` and, for PostgreSQL,
`DEADFIN_DATABASE_URL=postgres://...`. Restrict it to the operator account,
for example mode `0600`. Compose environment values are visible to people
who can inspect the container; an env file prevents accidental Git commits,
not access by a container administrator. Pin an image digest for reproducible
rollouts. Tune CPU, memory, process, and transcode-cache capacity for actual
simultaneous playback rather than treating the example limits as defaults.

For hardware transcoding, pass only the required GPU device and group access.
For Intel/VAAPI, start with `/dev/dri/renderD128` and the host render group;
use the full `/dev/dri` tree only if your device requires it. NVIDIA needs the
host's NVIDIA container runtime and selected GPU devices. Do not use
`privileged: true` or mount all host devices to make transcoding work. Test a
software stream first, then a single transcode, then concurrent transcodes.

A read-only root filesystem is a tested configuration. Deadfin writes only to
its data root and to `/tmp`, so add `read_only: true` together with a bounded
`tmpfs` for `/tmp`:

```yaml
    read_only: true
    tmpfs:
      - /tmp:size=4g
```

Size `/tmp` for your concurrent transcodes, then run a scan, an image fetch,
and a transcode to confirm.

## 4. Podman and NAS deployment

Rootless Podman can run the same OCI image and path layout. Example:

```sh
podman run -d --name deadfin --replace \
  --publish 127.0.0.1:8096:8096/tcp \
  --volume ./deadfin/data:/var/lib/deadfin:Z \
  --volume /srv/media:/media:ro,z \
  --security-opt no-new-privileges \
  --cap-drop ALL \
  ghcr.io/deadfin-org/deadfin:vX.Y.Z
```

SELinux `:Z` is for an exclusive mount and `:z` for a shared mount. Omit those
suffixes on systems without SELinux. Rootless UID mapping means host ownership
may differ from `65532`; use `podman unshare` or an idmapped mount to fix
permissions rather than making media world-writable. For a render device
accessed through a supplementary host group, Podman may need
`--group-add keep-groups` with the `crun` runtime. A rootless user service must
actually hold the group when it starts. Use a Quadlet or user systemd unit for
restart management; enable linger if it must run without a login session.

In an Unraid or TrueNAS app form, map the image's **data root** to durable local
or block-backed storage and media read-only. Map web files read-only only to
replace the bundled deadfin-web. Set the
container user or permissions for UID 65532. Publish 8096 to LAN or proxy and
7359/UDP only for discovery. Deadfin's layout differs from a Jellyfin app
template: one data root in place of `/config` and `/cache`, a fixed UID in
place of PUID/PGID, and no DLNA fields. Verify the rendered container
definition before first start. If a platform forces a shared or
privileged security context for GPU access, document that exception and keep
the media/data mounts as narrow as possible.

## 5. Kubernetes reference

Create the `deadfin` namespace, then use a storage class appropriate for the
cluster. The example
below shows the **API container**; it serves the bundled deadfin-web at
`/web/`. To serve jellyfin-web instead, put its files in `web/` on the data
PVC. It assumes a
PVC named `deadfin-data` and a read-only media PVC named `media-ro` already
exist. Use a block-backed ReadWriteOnce volume for SQLite, a single replica,
and a rolling strategy that never runs two Deadfin processes on the same data.
The `Recreate` strategy meets that condition.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: deadfin
  namespace: deadfin
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels: {app.kubernetes.io/name: deadfin}
  template:
    metadata:
      labels: {app.kubernetes.io/name: deadfin}
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile: {type: RuntimeDefault}
      containers:
        - name: deadfin
          image: ghcr.io/deadfin-org/deadfin:vX.Y.Z
          ports: [{name: http, containerPort: 8096}]
          securityContext:
            allowPrivilegeEscalation: false
            capabilities: {drop: [ALL]}
          readinessProbe:
            httpGet: {path: /System/Info/Public, port: http}
            periodSeconds: 10
          resources:
            requests: {cpu: 250m, memory: 512Mi}
            limits: {memory: 4Gi}
          volumeMounts:
            - {name: data, mountPath: /var/lib/deadfin}
            - {name: media, mountPath: /media, readOnly: true}
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: deadfin-data}
        - name: media
          persistentVolumeClaim: {claimName: media-ro}
---
apiVersion: v1
kind: Service
metadata:
  name: deadfin
  namespace: deadfin
spec:
  selector: {app.kubernetes.io/name: deadfin}
  ports: [{name: http, port: 8096, targetPort: http, protocol: TCP}]
```

Add the TMDb key or database URL with `secretKeyRef` if needed. Protect the
Secret with RBAC and encryption at rest; Kubernetes Secret data is not
automatically encrypted in etcd. Avoid an API ServiceAccount token unless an
actual integration needs the Kubernetes API. Set
`automountServiceAccountToken: false` on the pod, including when a web sidecar
shares it. `fsGroup` can cause an expensive recursive
ownership change on a large PVC; provision ownership ahead of time if needed.

Set ingress TLS, WebSocket support, stream timeouts, and buffering for your
controller. A proxy should pass `/socket` upgrades and should not buffer
`/Videos/` or `/Audio/` streams. Expose only 8096 (and a separate web service
if used). A Kubernetes Ingress is not a substitute for NetworkPolicy.

### NetworkPolicy for a shared namespace

Do **not** place a namespace-wide default deny into a shared namespace
without mapping every workload. Select Deadfin only, then allow named sources.
For a single-purpose Deadfin namespace, namespace-wide default deny can be
appropriate. Verify that the CNI actually enforces both directions.

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: deadfin-deny, namespace: deadfin}
spec:
  podSelector:
    matchLabels: {app.kubernetes.io/name: deadfin}
  policyTypes: [Ingress, Egress]
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: deadfin-allow, namespace: deadfin}
spec:
  podSelector:
    matchLabels: {app.kubernetes.io/name: deadfin}
  policyTypes: [Ingress, Egress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels: {kubernetes.io/metadata.name: ingress-nginx}
          podSelector:
            matchLabels:
              app.kubernetes.io/name: ingress-nginx
              app.kubernetes.io/component: controller # Confirm controller labels.
      ports: [{protocol: TCP, port: 8096}]
    # Add named same-namespace callers (Seerr, Radarr, Sonarr, etc.)
    # only when their integrations are configured and tested.
  egress:
    - to:
        - namespaceSelector:
            matchLabels: {kubernetes.io/metadata.name: kube-system}
          podSelector:
            matchLabels: {k8s-app: kube-dns} # Confirm your CoreDNS labels.
      ports:
        - {protocol: UDP, port: 53}
        - {protocol: TCP, port: 53}
    # Add a tested public HTTPS egress path for metadata providers.
```

This policy intentionally omits the site-specific public HTTPS rule. TMDb,
MusicBrainz, TheAudioDB, AniList, and optional scrobbling need outbound HTTPS.
Standard NetworkPolicy has no DNS-name match. A public CIDR allowance with
private/cluster ranges excluded can work, but its result can depend on service
NAT and the CNI. Use a tested egress proxy or a CNI with FQDN policy if you
need per-provider destinations. Do not apply the sample policy without adding
the required outbound rule and testing metadata/artwork. Likewise, add a
PostgreSQL egress peer on TCP 5432 if using an in-cluster database.

If a web sidecar serves port 8080, allow that port from the ingress controller.
Add a named caller, such as Seerr, Radarr, or Sonarr, when you configure its
integration. A sidecar reaches the API over loopback without a policy rule.
Keep ingress rules to named sources rather than `podSelector: {}`: CNIs differ
in how a broad ingress allowance interacts with egress rules. Test positive
and negative canary connections on your own CNI before cutover.

### Kubernetes rollout checks

1. Pin the image by digest. Apply the storage, Deployment, Service, and ingress
   through GitOps or a reviewed manifest. Confirm one ready pod.
2. Complete setup on a trusted path. Check `/System/Info/Public` and
   `/deadfin/v1/build`; do not use either endpoint alone as proof that playback
   works.
3. Test web sign-in, `/socket`, direct play, HLS transcode, image fetches,
   metadata refresh, subtitle access, and any required GPU path.
4. From a temporary test pod, check each intended caller and outbound
   provider. Check that Deadfin cannot reach an unrelated same-namespace
   canary on 8096/8080. Remove test workloads afterward.
5. Point each integration (Seerr, Arr webhooks) at Deadfin one at a time,
   and confirm that a real event arrives.

NetworkPolicy is additive. A second broad allow policy selecting Deadfin can
undo a narrow policy. A same-namespace label is not a strong trust boundary
against users who can create pods or change labels there; use RBAC and
separate namespaces for workloads with different trust levels.

## 6. Reverse proxy, first run, and client access

Deadfin serves plain HTTP on 8096. Use HTTPS at the proxy for browsers and
remote clients. Example Caddy site:

```caddyfile
media.example.com {
    reverse_proxy 127.0.0.1:8096
}
```

Give streams generous read/write timeouts and permit WebSocket upgrades.
Preserve the host and the full path. If using a path prefix such as
`/jellyfin`, set the matching base URL in Dashboard → Networking and restart;
test clients and webhook URLs, because some Jellyfin clients and integrations
do not handle a path prefix well.

Until setup completes, a direct local-network request can open the wizard.
Requests through a proxy or with forwarded-address headers require the
one-time setup link printed in Deadfin's log at `/deadfin/v1/setup?token=...`.
Treat the URL as a credential: do not put it in chat, analytics, screenshots,
or public logs. It changes on restart while setup is pending. Finish setup
before exposing the service beyond the trusted network. Behind a
TLS-terminating proxy, list the proxy in `KnownProxies` before the first
start, so the setup cookie is `Secure` when the proxy sends
`X-Forwarded-Proto: https`. Client identity stays the proxy address for
client-address rules and QuickConnect limits. Apply external rate limits at
the proxy if needed.

Use individual accounts. Give normal viewers only the libraries and
administrative rights they need. Keep API keys for automation separate and
rotate them if disclosed. Signing out revokes the client's token and ends
that device's sessions; a password change revokes the user's other tokens. If
a token is disclosed, revoke it and confirm that the old token gets 401.

## 7. Host and native Linux service

The image is the tested packaging route. A native build needs Go and libvips
development files; runtime needs libvips, `ffmpeg`, and `ffprobe` on `PATH`.
The release image includes Jellyfin FFmpeg and the supported Intel runtime.
For a native install, create a dedicated unprivileged `deadfin` account, a
private writable data directory, and read-only media access. Set
`DEADFIN_DATA_DIR` (or the fallback `JELLYFIN_DATA_DIR`) or `--program-data`, bind `--listen` to a trusted address,
and run under systemd with restart and resource limits. Test the exact FFmpeg
build and hardware drivers you install; a distribution's generic FFmpeg can
behave differently. Keep the service free of host root privileges and broad
write access. Package upgrades and rollback must preserve the data root.

## 8. Back up, upgrade, and recover

Before an upgrade, record the image digest and back up the full data root. For
SQLite, stop Deadfin or use a SQLite-consistent backup method; do not rely on
a raw copy of a live WAL database. For PostgreSQL, take `pg_dump` plus the data
root and record the database URL target. Restore both parts together. Test a
restore in an isolated instance with no shared writable media.

Change one version at a time. Confirm the startup migration, `/deadfin/v1/build`,
sign-in, library counts, one direct play, and one transcode. Watch errors,
memory, CPU, and cache use through a normal playback window. A schema
migration moves forward only, so the rollback path is the matching
pre-upgrade backup, or the untouched Jellyfin instance after a migration from
Jellyfin. Give Jellyfin and Deadfin separate config and database volumes.

## 9. Operations and security checklist

- Pin a release image and, for repeatable deployment, its digest. Review
  release notes and open security findings before wider exposure.
- Keep Deadfin state writable only by its service identity, on supported
  storage, with tested backups. Keep media read-only unless a chosen feature
  needs writes.
- Use TLS for remote clients; limit direct 8096 and optional 7359/UDP to the
  trusted network, on a bridge or pod network.
- Keep the process unprivileged. Drop capabilities, prevent privilege
  escalation, do not mount the container socket, and disable Kubernetes
  service account token mounting.
- Use explicit ingress callers and the smallest practical egress set. Test
  positive paths and a same-namespace negative canary; inspect all policies
  that select the pod.
- Protect setup links, credentials, database URLs, tokens, and backups.
  Disable profiling when finished.
- Test login, WebSocket, playback, transcode, images, metadata, and integrations
  after every version or network-policy change.

## References

- Deadfin [README](../../README.md), [Dockerfile](../../Dockerfile),
  [install guide](install.md), and [known differences](known-differences.md).
- [Docker Compose services reference](https://docs.docker.com/reference/compose-file/services/)
  and [Docker rootless mode](https://docs.docker.com/engine/security/rootless/).
- [Podman create reference](https://docs.podman.io/en/latest/markdown/podman-create.1.html).
- [Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/),
  [Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/),
  and [Secret practices](https://kubernetes.io/docs/concepts/security/secrets-good-practices/).
- [Jellyfin container patterns](https://jellyfin.org/docs/general/installation/container/)
  and [reverse proxy guidance](https://jellyfin.org/docs/general/post-install/networking/reverse-proxy/)
  for client-facing behavior; Deadfin differences above take precedence.
