# Native reference provenance

All references were obtained from the original C++ source in
`github.com/pg9182/tf2lzham` version `v0.0.4`, upstream commit
`c11a1f50ebc85f9290ede436a1f8e2f34b646be0`.
Before publication, all 46 files of the original vendored reference source were
compared byte-for-byte with that public upstream commit and matched. Rebuilding
the reference tools does not require the parent project used during the port.
Reference platform: Ubuntu 22.04, x86-64, GCC/G++ 11.4, `-O2 -DNDEBUG` and
`-DLZHAM_ANSI_CPLUSPLUS`, no helper threads.

These C++ files are **optional test tools**, not part of the Go package build or
the normal test command. They link against the archived original implementation,
not a second codec included at runtime.

## Data

- `../compress.json`: 125 original memory-compressor results, including the
  input hash, complete compressed-stream hash, length and checksums. Inputs are
  reconstructed in `referenceInputs` in `reference_test.go`.
  The original and Go compressed files were also compared byte-for-byte; every
  Go stream was decoded by the original and compared to its input.
- `../config-{0,1,2,3}.lzham` and `../decode.json`: forced-decision fixtures made
  by `fixture_generator.cpp`, using the original `lzcompressor::state::encode`
  and `symbol_codec`. Each fixture contains five compressed blocks (flush modes
  0, 1, 2), high/low-entropy literals long enough to rescale frequencies,
  short/full/repeat/delta decisions, distance slots, huge lengths
  258/513/514/1537/1538/5633/5634/65536 and partial state reset.
  Two additional blocks create a 1 MiB history and exercise both ends of every
  distance slot. Config bits select fast updates and polar coding independently.
  The original memory decoder verified each fixture against its generated
  `.raw` source before hashes were frozen.
  `TestReferenceSymbolEncoder` rebuilds the same forced decisions with Go and
  compares every compressed byte, in addition to the decoder's output hashes.
- `../errors.json`: 770 original Go-wrapper outcomes. The input bytes and output
  capacity are stored in each case. Sources are truncations and one-bit changes
  of the original `hello` and 256-byte random streams, capacity boundaries and
  targeted sync/EOF inputs. Zero buffers reproduce the original Go guard
  without calling C. Errors return zero for all numeric fields.

Golden files must not be regenerated from the pure-Go encoder under test.
Native undefined behavior, such as out-of-bounds reads on malformed streams,
is not a compatibility requirement.

## Rebuild the optional reference tools (Linux)

Run from this package. Git downloads the pinned public upstream source:

```sh
SRC=$(mktemp -d)
git clone --quiet https://github.com/pg9182/tf2lzham.git "$SRC"
git -C "$SRC" checkout --quiet --detach c11a1f50ebc85f9290ede436a1f8e2f34b646be0
g++ -std=c++11 -O2 -DNDEBUG -DLZHAM_ANSI_CPLUSPLUS -I"$SRC" \
  "$SRC"/*.cpp testdata/oracle/memory_oracle.cpp -o /tmp/tf2lzham-oracle
g++ -std=c++11 -O2 -DNDEBUG -DLZHAM_ANSI_CPLUSPLUS -I"$SRC" \
  "$SRC"/*.cpp testdata/oracle/fixture_generator.cpp -o /tmp/tf2lzham-fixtures
/tmp/tf2lzham-oracle c input.bin compressed.lzham
/tmp/tf2lzham-oracle d compressed.lzham decoded.bin
for config in 0 1 2 3; do
  /tmp/tf2lzham-fixtures "$config" "/tmp/config-$config"
  cmp "testdata/config-$config.lzham" "/tmp/config-$config.lzham"
  /tmp/tf2lzham-oracle d "/tmp/config-$config.lzham" "/tmp/config-$config.out"
  cmp "/tmp/config-$config.raw" "/tmp/config-$config.out"
done
```

The memory tool accepts an optional destination capacity for `d`, or compression
flags and level for `c`; without those arguments it uses the original wrapper.
It prints native checksum values and errors without normalizing the codec.
