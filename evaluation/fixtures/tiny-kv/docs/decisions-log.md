# Decisions log

| ID | Decision | Rationale |
| --- | --- | --- |
| KV-D1 | Append-only log, not in-place updates. | Crash safety: a torn write only loses the last record, which the CRC detects. |
| KV-D2 | `fsync` after every `put`. Batching was considered and rejected for v1. | K1 durability matters more than write throughput for CLI-sized data. |
| KV-D3 | Compact when the log exceeds 4x live size, by rewrite-and-rename. | Rename is atomic on POSIX, so readers never see a half-compacted file. |
