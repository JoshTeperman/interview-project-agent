"""Append-only key-value store (see docs/spec.md)."""
import os
import struct
import zlib

COMPACT_RATIO = 4  # KV-D3


class Store:
    def __init__(self, path):
        self.path = path
        self.index = {}
        self.live_bytes = 0
        self._load()
        self.f = open(path, "ab")

    def put(self, key: bytes, value: bytes) -> None:
        rec = struct.pack(">II", len(key), len(value)) + key + value
        rec += struct.pack(">I", zlib.crc32(rec))
        offset = self.f.tell()
        self.f.write(rec)
        self.f.flush()
        os.fsync(self.f.fileno())  # KV-D2: durable before returning (K1)
        self.index[key] = offset
        self.live_bytes += len(rec)
        if self.f.tell() > COMPACT_RATIO * self.live_bytes:
            self.compact()

    def get(self, key: bytes):
        offset = self.index.get(key)
        if offset is None:
            return None
        with open(self.path, "rb") as f:
            f.seek(offset)
            klen, vlen = struct.unpack(">II", f.read(8))
            f.read(klen)
            return f.read(vlen)

    def compact(self) -> None:
        tmp = self.path + ".tmp"
        # Rewrite live records, then rename atomically (KV-D3).
        raise NotImplementedError("compaction lands in v1.1")

    def _load(self) -> None:
        if not os.path.exists(self.path):
            return
