# tiny-kv spec

## Requirements

- **K1** `put(key, value)` is durable once it returns.
- **K2** `get(key)` returns the latest value or `None`.
- **K3** The data file never grows beyond 4x the live data size.

## Design

Records are appended to `data.log` as `len(key) len(value) key value crc32`.
The index maps each key to the offset of its latest record. Compaction rewrites
live records to `data.log.tmp` and renames it over `data.log` (see KV-D3).
