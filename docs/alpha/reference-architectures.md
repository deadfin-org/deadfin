# Reference architectures

Two tested layouts for a home install: one server, and two servers with a
floating address. Both serve clients on `https://media.example.com:8920`, as
[install.md](install.md#external-access) describes. The addresses below are
examples. Use your own.

| | Single node | Two-node HA |
|---|---|---|
| Hosts | 1 | 2 |
| Database | SQLite or PostgreSQL | PostgreSQL (required for replicas) |
| Shared storage | none | data root on NFS, read-write on both hosts |
| TLS proxy | none on a LAN; **required** for remote access (Caddy on the same host) | Caddy on both hosts, one shared certificate |
| Client address | the host | a floating IP (keepalived) |
| Survives | nothing: a host restart stops playback | a proxy failure, a host failure, and a rolling update (running transcodes only with a transcode worker) |
| Minimum version | any | v0.4.0-rc.1 (server replicas) |

## 1. Single node

```
clients ──:8920──▶ router port forward ──▶ host
                                           ├─ Caddy :8920 (TLS, DNS-01)
                                           └─ Deadfin :8096 (GPU, /dev/dri)
                                                │
                              media (read-only) ┘  NAS or local disk
```

One host runs Deadfin. It also runs Caddy when clients connect from outside
your network.

> [!WARNING]
> **External access needs a TLS reverse proxy.**
> Deadfin serves plain HTTP on port 8096. It does not terminate TLS.
> **Never forward port 8096 to the internet.** Passwords and session tokens
> would cross the internet in clear text, and the setup page would be
> exposed ([deployment-security.md](deployment-security.md#exposure-decision)).
>
> - **LAN only:** no proxy is needed. Clients use `http://<host>:8096`.
> - **Remote access for your own devices:** use a VPN (WireGuard,
>   Tailscale). It has the smallest exposure, and nothing is published.
> - **Remote access for other people or for TV apps:** put a maintained
>   reverse proxy with HTTPS in front, and forward only its port (8920).
>   This guide uses Caddy because it gets and renews the certificate by
>   itself. nginx and Traefik work the same way.

The rest of this section assumes remote access through Caddy.

- **Database:** SQLite in the data root is enough for one process. Use
  PostgreSQL if you plan to add a second server later. A move between the
  two stores needs a fresh install and a new import.
- **Media:** mount it read-only. Deadfin never writes to media folders.
- **GPU:** pass `/dev/dri` through. Add the group that owns
  `/dev/dri/renderD128` on the host (`video` or `render`; check with
  `ls -ln /dev/dri`) with `group_add`.
- **TLS:** run Caddy on port 8920 with the ACME DNS challenge, because
  port 80 stays closed. Build Caddy with the plugin for your DNS provider
  (`xcaddy build --with github.com/caddy-dns/<provider>`).
- **Proxy address:** list the address that Deadfin sees for Caddy in
  `KnownProxies`. When Caddy runs in the same Compose project, that is the
  subnet of the Compose network, for example `172.18.0.0/16`. After you
  sign in through the proxy, check Dashboard → Sessions. It must show the
  client address, not the proxy address.
- **Backups:** the data root (`/var/lib/deadfin`), and the database when it
  is PostgreSQL.

Example `compose.yaml`:

```yaml
services:
  deadfin:
    image: ghcr.io/deadfin-org/deadfin:v0.4.0-rc.2@sha256:<digest>
    restart: unless-stopped
    env_file: .env                # TMDB_API_KEY, optional DEADFIN_DATABASE_URL
    devices:
      - /dev/dri:/dev/dri
    group_add:
      - "44"                      # the group that owns renderD128 on this host
    volumes:
      - ./data:/var/lib/deadfin
      - /mnt/media:/mnt/media:ro
  caddy:
    image: <your caddy build with caddy-dns/<provider>>
    restart: unless-stopped
    ports:
      - "8920:8920"
    env_file: caddy.env           # DNS provider API token
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data
volumes:
  caddy-data:
```

`Caddyfile`:

```
{
	auto_https disable_redirects
}

media.example.com:8920 {
	tls {
		dns <provider> {env.DNS_API_TOKEN}
	}
	reverse_proxy deadfin:8096 {
		flush_interval -1
	}
}
```

## 2. Two-node HA

```
clients ──:8920──▶ router port forward ─┐
LAN clients (local DNS) ────────────────┤
                                        ▼
                    floating IP 10.0.60.10 (keepalived, DMZ VLAN)
            ┌───────────────────────┴───────────────────────┐
  node A (preferred)                               node B
  Caddy :8920 on the floating IP                   Caddy :8920 on the floating IP
  Deadfin replica  :8096                           Deadfin replica  :8096
            │      └──── replica traffic, private network ────┘      │
            └──────────── PostgreSQL (primary/replica) ──────────────┘
                 shared data root (NFS)   media (read-only NFS)
```

Each of the two hosts runs a Deadfin replica and Caddy. keepalived moves one
floating IP between the hosts. Clients and transcode workers use only the
name of the floating IP.

### Network

- Give each host two networks. The **client-facing** network (this layout
  uses a DMZ VLAN) carries Caddy and the floating IP. The **private**
  network carries the database, NFS and replica traffic.
- If the client-facing network has its own gateway, add source-based
  routing. Replies from that network must leave through its gateway. If
  they use the default route, the router drops them as asymmetric. With
  netplan:

  ```yaml
  vlans:
    bond0.60:
      id: 60
      link: bond0
      addresses: [10.0.60.11/24]
      routes:
        - {to: default, via: 10.0.60.1, table: 60}
        - {to: 10.0.60.0/24, scope: link, table: 60}
      routing-policy:
        - {from: 10.0.60.0/24, table: 60, priority: 100}
  ```

  The rule covers the floating IP too, because it is in the same subnet.

- On Broadcom NetXtreme-E NICs (`bnxt_en`), run a kernel with upstream fix
  `a51d233ccd48` (*bnxt_en: fix DMA mapping length for padded small
  packets*) before you send traffic on a VLAN interface. The fix is in
  Linux 7.3, 7.2.y, 6.18.y and 6.12.y, and in Ubuntu 26.04 from
  `7.0.0-43`. Without it, short frames such as the keepalived VRRP adverts
  cause IOMMU faults
  (`DMAR: [DMA Read NO_PASID] … PTE Read access is not set`), and the NIC
  stops transmitting.

### Database

Both replicas use one PostgreSQL database. A primary/replica pair works
through a multi-host URL, so no VIP or HAProxy is needed in front of it:

```
DEADFIN_DATABASE_URL=postgres://deadfin:<pw>@10.0.50.62:5432,10.0.50.63:5432/deadfin?target_session_attrs=read-write
```

`target_session_attrs=read-write` makes the driver pick the current
primary. This setup was tested on PostgreSQL 18 with Patroni.

### Replicas

- Set `DEADFIN_REPLICA_URL` on each replica to its own address on the
  private network, for example `http://10.0.50.11:8096` and
  `http://10.0.50.12:8096`. Replica requests are signed with a key that
  Deadfin keeps in the database.
- **Data root:** share one data root between the replicas. It holds the
  settings (`config/*.xml`), the library folders (`root/default`),
  collections, playlists, trickplay files and local images (`metadata`).
  A data root for each replica is not safe. A replica that reads the
  library list and does not find the folder of a library on its own disk
  deletes that library from the database. The other replica then adds the
  library again with a new ID. Deadfin does not copy images or other files
  between replicas.

  The NAS exports the data root over NFS v4.2, read-write for both hosts,
  at the same path on both. Deadfin writes each settings file to a
  temporary file and then renames it, so a replica never reads half a file.

  - **Block a start on an empty data root.** When the NFS share is not
    mounted, the mount point is an empty local folder. A replica that
    starts on it deletes the libraries, as described above. Bind a
    *subfolder* of the share with `create_host_path: false` (see the
    example below). When the share is not mounted, the subfolder does not
    exist, and Docker refuses to start the container. Order Docker after
    the mount: put `After=` and `Wants=` for the mount unit in a
    `docker.service` drop-in.
  - With root squash on the export, Docker checks the bind source as
    `nobody`. Give the top folder of the share execute permission for
    others (`0751`) so that the check passes. The folders inside the data
    root stay `0700`.
  - Keep `log/` on the local disk of each host. Both replicas give their
    log files the same names.

  Two limits apply in v0.4.0:

  - **Settings reach the other replica only when it restarts.** Each
    replica reads the settings files once, at start. A later save on the
    other replica writes its old copy back over the change. Make settings,
    library and plugin changes on one replica, then restart the other one.
    Library folders and library options are read from disk each time, so
    both replicas see them at once.
  - **Both replicas run the scheduled tasks** (library scan every 12 hours,
    metadata refresh, trickplay, cleanup). The schedule is shared, so you
    cannot stagger the tasks. Start a manual scan on one replica only.
- **Transcode directory:** `cache/transcodes` is inside the shared data
  root, so both replicas share it. A remote HLS job then moves to the other
  replica with no restart. To use a separate export for the cache, mount it
  on `cache` (SSD, no snapshots, sync off, owned by the container user,
  65532).

  > [!WARNING]
  > Do not mount a separate export on `cache/transcodes`. At start, Deadfin
  > clears the transcode directory by deleting the directory itself. This
  > fails on a mount point, and the replica stops with
  > `device or resource busy`. Mount on `cache` instead.

  "Remote" means that the job runs on a
  [transcode worker](../transcode-workers.md#5-two-server-replicas). With no
  worker, each replica runs FFmpeg itself (the default `local` placement
  mode). That job stops when its replica stops or its host fails, whether
  or not the directory is shared. The client must then start playback
  again. To keep transcodes running through a failover or a rolling
  update, add at least one worker that is not on a replica host. Point the
  worker at the name of the floating IP, and set
  `DEADFIN_TRANSCODE_WORKER_MODE=workers-first`.

  ```yaml
  services:
    deadfin:
      ports:
        - "10.0.50.11:8096:8096"   # this host's private address; 10.0.50.12 on node B
      environment:
        DEADFIN_REPLICA_URL: http://10.0.50.11:8096   # http://10.0.50.12:8096 on node B
      volumes:
        - type: bind                  # shared data root (NFS), see above
          source: /mnt/deadfin-data/deadfin
          target: /var/lib/deadfin
          bind:
            create_host_path: false   # share not mounted: the container does not start
        - ./log:/var/lib/deadfin/log  # per host
        - /mnt/deadfin-transcodes:/var/lib/deadfin/cache
        - /mnt/media:/mnt/media:ro
  ```

  Publish port 8096 on the private address only. Caddy on the host and the
  other replica reach the container through this mapping. Without it, the
  proxy returns 502 and requests between replicas fail.

### Caddy on both nodes

- Run Caddy on the host, not in Compose, so that it can bind the floating
  IP. Set `net.ipv4.ip_nonlocal_bind=1` so that the node without the
  floating IP can bind it too, and keep Caddy running on that node.
- Both nodes need the same certificate. Keep the Caddy storage in
  PostgreSQL with a storage module that locks
  (`github.com/dmarcwise/caddy-storage-postgresql`, which uses pgx and
  accepts the same multi-host URL). Then only one node renews at a time.
  This matters with DNS providers whose API replaces the whole zone on
  every write (Namecheap `setHosts`): two nodes that renew at once can
  overwrite the records of each other.
- If your public domain is also your internal DNS zone (split horizon), set
  public `resolvers` in the `tls` block. Otherwise Caddy checks the
  challenge record against your internal DNS and waits forever.
- Pass the database password in `PGPASSWORD`, not in the URL. Caddy logs
  the storage URL at info level when it cleans storage.

```
{
	storage postgresql {
		dsn {env.CADDY_PG_DSN}        # postgres://caddy@10.0.50.62:5432,10.0.50.63:5432/caddy?target_session_attrs=read-write
	}
	auto_https disable_redirects
	default_bind 10.0.60.10
}

media.example.com:8920 {
	tls {
		dns <provider> { ... }
		resolvers 1.1.1.1 9.9.9.9
	}
	reverse_proxy 10.0.50.11:8096 10.0.50.12:8096 {
		lb_policy first               # node A first; node B when A fails its health check
		lb_try_duration 5s            # retry a failed connection on the other replica
		lb_try_interval 250ms
		fail_duration 30s             # passive check: a failed request marks the replica down
		health_uri /System/Info/Public
		health_interval 10s
		flush_interval -1
	}
}
```

- The active health check alone leaves a gap. If the replica or the host
  of node A fails between two probes, `lb_policy first` keeps choosing it,
  and Caddy returns 502 until the next probe, although node B is up. By
  default, Caddy does not retry and does not track failures.
  `lb_try_duration` makes it try the other replica when a connection
  fails. `fail_duration` takes a replica out of rotation after a failed
  request. A move of the floating IP does not close the gap, because the
  Caddy on both nodes picks from the same list.
- `KnownProxies`: the private-network addresses of both hosts
  (`10.0.50.11`, `10.0.50.12`). Caddy reaches the replicas over the private
  network, so these are the addresses that Deadfin sees for Caddy. The
  setting is in the shared data root: set it once, then restart the other
  replica.

### keepalived

```
global_defs {
	enable_script_security
	script_user root
}
vrrp_script chk_caddy {
	script "/usr/bin/systemctl is-active --quiet caddy"
	interval 3
	fall 2
	rise 2
}
vrrp_instance DEADFIN_EDGE {
	state BACKUP
	interface bond0.60
	virtual_router_id 60
	priority 150                     # node A; 100 on node B
	advert_int 1
	unicast_src_ip 10.0.60.11        # node B: 10.0.60.12
	unicast_peer {
		10.0.60.12                   # node B: 10.0.60.11
	}
	authentication {
		auth_type PASS
		auth_pass <8 chars>
	}
	virtual_ipaddress {
		10.0.60.10/24 dev bond0.60
	}
	track_script {
		chk_caddy
	}
}
```

Measured failover on the tested pair:

- Stopping Caddy on node A moved the floating IP to node B. Node B served
  requests again within 4 s, with the same certificate. The IP returned to
  node A 7 s after Caddy started there again.
- Stopping the Deadfin replica on node A (`docker compose stop`, as in a
  rolling update) while requesting `/System/Info/Public` through the
  floating IP every 0.5 s: 90 of 90 requests answered 200 during the 16 s
  that the replica was down. The replica logged
  `replica drained; its sessions and jobs are free for the peers` before it
  stopped.

### Contain the replica (optional)

The client-facing network alone does not isolate the Deadfin container,
because the host is still on the private network. Filter the outbound
traffic of the container with nftables, matched on the Compose bridge
(`driver_opts: com.docker.network.bridge.name: br-deadfin`):

- allow replies, PostgreSQL, the port of the peer replica, and all
  addresses outside the private ranges (metadata providers);
- drop the rest of the private network (NAS, DNS servers, other hosts).

Name resolution: on a user-defined Compose network, the embedded DNS server
of Docker forwards lookups to the resolvers of the host, from the host
itself. The filter does not see these lookups. Metadata names resolved with
the filter loaded. If the container sends DNS straight to a server (a
`dns:` entry in Compose, or another runtime), allow UDP and TCP port 53 to
that resolver before the drop rule. Otherwise metadata and image requests
fail.

Load the rules from their own table and a systemd unit that starts before
`docker.service`. Rules in `DOCKER-USER` are lost at every reboot, and
`nftables.service` flushes the tables of Docker.

### Intel iGPU nodes

On Alder Lake iGPUs (UHD 770), Ubuntu leaves GuC submission off. HEVC
10-bit decode together with a QSV encode then hangs the video engine
(`GPU HANG … hevc_qsv`). Add `i915.enable_guc=3` to the kernel command
line. Gen12 iGPUs decode AV1 but cannot encode it.

### Replicas with different GPUs

The encoding settings are in the shared data root, so both replicas use the
same settings. In v0.4.0-rc.1, a replica does not check its own encoder
before it chooses a codec. With **Allow AV1 encoding** on, a replica whose
GPU cannot encode AV1 (a UHD 770 next to an Arc A380, for example) starts
`av1_qsv` for a client that prefers AV1. FFmpeg fails
(`Current profile is unsupported`), and the segment request returns 500.
Use GPUs with the same encoders on both replicas, or turn off AV1 encoding.
