# Oblivion CMS (codename)

A small CMS for a personal site or a blog. Go backend, React admin panel, everything is a plugin.
One static binary, SQLite, no CGO. It runs on 1 CPU and 1 GB of RAM.
License: AGPL-3.0-or-later. Status: beta, the plugin API (`sdk/`) is unstable.

The public site is in Russian. The admin panel is in English.

## Install with the helper (recommended)

The helper `oblivionctl` runs on the host and drives Podman (or Docker). It never touches the database
itself: every action on the data is a one-shot container of the same image.

```sh
make image build          # builds the image and bin/oblivion, bin/oblivionctl
bin/oblivionctl install --admin YOUR_LOGIN --proxy 127.0.0.1
```

`install` asks for the first administrator's password (no echo, standard input only), creates the volume,
starts a hardened container (read-only, non-root, no capabilities, 512 MB, port bound to 127.0.0.1,
restart `unless-stopped`) and waits for `/healthz`.

- Put a reverse proxy with HTTPS in front of it. Cookies are marked `Secure` and do not work over plain HTTP.
  For a local try-out use `--insecure-http`.
- Podman: to start the container after a reboot run `systemctl --user enable --now podman-restart.service`
  and `loginctl enable-linger $USER`.

Day-to-day commands:

| Command | What it does |
|---|---|
| `oblivionctl status`, `logs [--tail N] [-f]` | state and container log |
| `oblivionctl user list` / `create LOGIN admin\|editor\|author` | accounts |
| `oblivionctl user reset-password LOGIN` | lost password; works while the server runs |
| `oblivionctl user disable LOGIN` / `enable LOGIN` | the last active administrator cannot be disabled |
| `oblivionctl config show` / `set KEY=VALUE` | `SECURE_COOKIE`, `TRUSTED_PROXIES`, `LOG_LEVEL`; restarts and checks health |
| `oblivionctl backup [FILE]` | consistent snapshot (SQLite `VACUUM INTO` plus media) as `.tar.gz` |
| `oblivionctl restore FILE [--replace]` | restore into an empty volume; `--replace` saves the current data first |
| `oblivionctl update [--image REF]` | backup, switch image, health check; the old container comes back on failure |

State of the helper is in `$XDG_CONFIG_HOME/oblivion/ctl.json` (mode 0600).
After a password reset the sign-in throttle of that account may delay the next attempt for up to two minutes.

## Install by hand

```sh
podman volume create oblivion-data
podman run -d --name oblivion --restart unless-stopped \
  --read-only --cap-drop=ALL --security-opt no-new-privileges --memory 512m \
  -p 127.0.0.1:8080:8080 -v oblivion-data:/data \
  -e OBLIVION_TRUSTED_PROXIES=127.0.0.1 \
  localhost/oblivion:dev
podman logs oblivion      # prints a one-time setup token (valid one hour) while no user exists
```

Open `https://your-site/admin/` and create the first administrator with that token.
Accounts and backups are then managed with the helper, or by one-shot containers of the same image:
`podman run --rm -i -v oblivion-data:/data IMAGE user list` (the password is read from standard input only).

## First steps in the admin panel

1. Settings: set `base_url` to the public address, for example `https://example.com` (no path).
   Without it the sitemap, feed and canonical links are not correct.
2. Write a post. The field "Author" is the public nickname: Latin letters, digits, space and `. _ -`, 2 to 32 characters.
   It can never equal or contain the login of any account, so logins are not published.
3. Passwords: printable ASCII only (no Cyrillic), at least 15 characters. The rule is applied when a password is set.

## Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `OBLIVION_LISTEN` | `127.0.0.1:8080` (`0.0.0.0:8080` in the image) | listen address |
| `OBLIVION_DATA_DIR` | `./data` (`/data` in the image) | SQLite file and media |
| `OBLIVION_SECURE_COOKIE` | `true` | set `false` only for plain HTTP development |
| `OBLIVION_TRUSTED_PROXIES` | empty | IPs or CIDRs whose `X-Forwarded-For` is trusted |
| `OBLIVION_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `OBLIVION_LOG_JSON` | `false` (`true` in the image) | JSON log lines |

`TRUSTED_PROXIES` matters: without it every visitor behind your proxy has the proxy's address, so the sign-in
throttle treats all visitors as one client. The server logs a warning when it sees `X-Forwarded-For` from a peer it does not trust.
Set it to the address your proxy connects from. If you are not sure which one it is, look at the `client` value in the warning above; I have not verified which address a proxy on the same host presents to a rootless Podman container.

## Backup, restore, update

- Backup: `oblivionctl backup /path/backup.tar.gz`. It is safe while the server runs. Keep copies off the host.
- Restore: `oblivionctl restore /path/backup.tar.gz` into a new volume; `--replace` overwrites existing data after saving it.
  The archive is checked (checksum, one database, no traversal or links) and the database is integrity-checked before use.
- Update: `oblivionctl update`. Always makes a backup first.

## Sign-in blocks in the log (fail2ban)

When the throttle blocks a sign-in the server writes one warning per block. For a blocked network it includes the client address:

```
level=WARN msg="sign-in blocked" plugin=auth kind=network client=203.0.113.9 retry_after=2m0s
```

With JSON logs (the image default) the same fields are JSON keys. Blocks of a single username (`kind=username`) have no address
and no login name. This line is the only place where a visitor's IP address is written; the access log of successful
`/healthz` checks is silenced.

A fail2ban filter (`/etc/fail2ban/filter.d/oblivion.conf`) for the JSON form:

```
[Definition]
failregex = "msg":"sign-in blocked".*"kind":"network".*"client":"<HOST>"
```

I have only checked the shape of the pattern against sample lines, not inside a running fail2ban. Test it with `fail2ban-regex`.
The built-in throttle already slows guessing; fail2ban is an extra layer, not a requirement.

## TLS notes

The application sends `Strict-Transport-Security: max-age=63072000` (no `includeSubDomains`, no `preload`).
Terminate TLS in your proxy. Cookies use the `__Host-` prefix, so the site must be served from the root of its own host name.

## Develop

```sh
make check        # vet and race tests, required before every commit
make admin        # build the SPA (Node, npm ci)
make run          # admin + build + run with OBLIVION_SECURE_COOKIE=false
cd admin && npm run typecheck
```

Official plugins may import only `sdk/` from the kernel (enforced by `plugins/boundary_test.go`).
