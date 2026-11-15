#!/usr/bin/env python3
"""Real libtorrent 2.x LEECHER used by cmd/udinteropx (BX-06, ADR-0021 path A).

This is an OPTIONAL external tool, not a project dependency: it requires the
``libtorrent`` Python binding installed outside the repository (for example
``python3 -m pip install --target /tmp/udlt libtorrent``). CI never runs it and
the Go build has no knowledge of it. Without the binding this script reports
UNSUPPORTED instead of faking a result.

Unlike tools/interop_libtorrent.py (which SEEDS), this script DOWNLOADS from
this project's background seeding session. The Go harness stops every other
seeder first and injects ud's listen port, so a hash-verified payload proves
that ud actually uploaded over the real BitTorrent wire protocol. This is
still a ONE-WAY, loopback, self-hosted-tracker supplementary case -- never an
L4 PASS (no packet capture, no isolated gigabit LAN, no named tracker binary,
no bidirectional qBittorrent / Transmission matrix).

Wire protocol with cmd/udinteropx:

* argv: ``--torrent <file> --out-dir <dir> --peer-port <int>
         --expected-sha256 <hex> --timeout <seconds>``
* stdout: exactly one JSON line, then exit:
  ``{"status":"PASS","engine":"libtorrent <version>",...}``
  or ``{"status":"FAIL"/"UNSUPPORTED","reason":"..."}``
* stderr: free-form diagnostics (captured by the Go harness).
"""

import argparse
import hashlib
import json
import os
import sys
import time
import warnings


def emit(obj):
    sys.stdout.write(json.dumps(obj, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def main():
    parser = argparse.ArgumentParser(description="libtorrent loopback leecher")
    parser.add_argument("--torrent", required=True)
    parser.add_argument("--out-dir", required=True)
    parser.add_argument("--peer-port", required=True, type=int)
    parser.add_argument("--expected-sha256", required=True)
    parser.add_argument("--timeout", required=True, type=float)
    args = parser.parse_args()

    try:
        import libtorrent as lt
    except Exception as exc:  # optional dependency: never claim a PASS here
        emit({"status": "UNSUPPORTED", "reason": "python libtorrent binding unavailable: %s" % exc})
        return 0

    try:
        info = lt.torrent_info(args.torrent)
        settings = {
            "listen_interfaces": "127.0.0.1:0",
            "enable_dht": False,
            "enable_lsd": False,
            "enable_upnp": False,
            "enable_natpmp": False,
            "announce_to_all_trackers": True,
        }
        session = lt.session(settings)
        params = lt.add_torrent_params()
        params.ti = info
        params.save_path = args.out_dir
        params.flags |= lt.torrent_flags.disable_pex
        params.flags &= ~lt.torrent_flags.paused
        params.flags &= ~lt.torrent_flags.auto_managed
        handle = session.add_torrent(params)
        handle.connect_peer(("127.0.0.1", args.peer_port))

        deadline = time.time() + args.timeout
        status = None
        while time.time() < deadline:
            status = handle.status()
            if status.is_seeding and not status.paused:
                break
            if status.errc.value() != 0:
                emit({"status": "FAIL", "reason": "libtorrent error: %s" % status.errc})
                return 1
            time.sleep(0.05)
        if status is None or not (status.is_seeding and not status.paused):
            emit({"status": "FAIL", "reason": "timed out downloading from ud seeding session (state=%s)" % (status.state if status else "none")})
            return 1

        # The seeded torrent is single-file; verify the exact bytes on disk.
        path = os.path.join(args.out_dir, info.name())
        with open(path, "rb") as fh:
            data = fh.read()
        digest = hashlib.sha256(data).hexdigest()
        ok = digest == args.expected_sha256
        peer_id = ""
        try:
            with warnings.catch_warnings():
                warnings.simplefilter("ignore", DeprecationWarning)
                raw = session.id()
            peer_id = raw.to_bytes(20, "big").hex() if isinstance(raw, int) else str(raw)
        except Exception:
            pass
        emit({
            "status": "PASS" if ok else "FAIL",
            "engine": "libtorrent " + lt.__version__,
            "sha256": digest,
            "expected_sha256": args.expected_sha256,
            "bytes": len(data),
            "state": str(status.state),
            "total_download": status.total_download,
            "total_upload": status.total_upload,
            "num_peers": status.num_peers,
            "peer_port": args.peer_port,
            "peer_id": peer_id,
            "reason": "" if ok else "sha256 mismatch",
        })
        return 0 if ok else 1
    except Exception as exc:
        emit({"status": "FAIL", "reason": "libtorrent leecher failed: %s: %s" % (type(exc).__name__, exc)})
        return 1


if __name__ == "__main__":
    sys.exit(main())
