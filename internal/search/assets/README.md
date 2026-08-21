# Embedded ripgrep

These platform binaries are unmodified `ripgrep` 15.2.0 release artifacts from
https://github.com/BurntSushi/ripgrep/releases/tag/15.2.0. The corresponding
license texts are `LICENSE-MIT`, `UNLICENSE`, and `COPYING` in this directory.

The build embeds only the artifact selected by the target `GOOS/GOARCH`. At
runtime ki materializes it in the user's cache and executes it directly.

SHA-256:

| Target | File | SHA-256 |
|---|---|---|
| linux/amd64 | `rg-linux-amd64` | `e62198eb19b136b88c330af83647b5a962cb99b6b1f066758568f12de1974849` |
| linux/arm64 | `rg-linux-arm64` | `c14cdb389f34e504d69e386cfc67d5c5d9a730a990de03ca6910b2a15e30386a` |
| darwin/amd64 | `rg-darwin-amd64` | `0c9a0066db0d26b640777db88045b0ccdd58509a746700e43e1c4ff8707a5ed0` |
| darwin/arm64 | `rg-darwin-arm64` | `a326a1fb48074202e9ad41e4cd1e389eeea372c8c6f7d7e80da81176d5d9430e` |
| windows/amd64 | `rg-windows-amd64.exe` | `14231169855ec5205cf5a1b6f1db358ff4aed4247c86b69ce8aae647c77f6680` |
| windows/arm64 | `rg-windows-arm64.exe` | `d33a29a9ef03c9f4c03be9e8d88498e6e2d2e566d64cdbdef97f9afc8f13120c` |
