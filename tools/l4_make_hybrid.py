#!/usr/bin/env python3
"""Create a real BEP 52 v1+v2 hybrid .torrent with python-libtorrent 2.x.

Usage: l4_make_hybrid.py <payload_path> <announce_url> <output_torrent>
libtorrent 2.x defaults create_torrent to hybrid (both v1 and v2 hashes)
unless v1_only / v2_only is explicitly set. Lab-only helper.
"""
import os
import sys
import libtorrent as lt


def main():
    payload, announce, out = sys.argv[1], sys.argv[2], sys.argv[3]
    fs = lt.file_storage()
    fs.add_file("payload.bin", os.path.getsize(payload))
    # No v1_only / v2_only -> hybrid (v1 pieces + v2 merkle file tree).
    t = lt.create_torrent(fs, 16384)
    t.set_creator("ud-l4-hybrid")
    t.add_tracker(announce)
    lt.set_piece_hashes(t, os.path.dirname(payload))
    with open(out, "wb") as f:
        f.write(lt.bencode(t.generate()))


if __name__ == "__main__":
    main()
