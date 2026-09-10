# Embedded search executables

These platform binaries are unmodified release artifacts. The build embeds only
the artifact selected by the target `GOOS/GOARCH`. At runtime ki materializes
the embedded rg and fd into a shared tools directory and both executes them and
puts that directory on the shell PATH.

## ripgrep 15.2.0

From https://github.com/BurntSushi/ripgrep/releases/tag/15.2.0. License texts
are `LICENSE-MIT`, `UNLICENSE`, and `COPYING` in this directory.

| Target | File | SHA-256 |
|---|---|---|
| linux/amd64 | `rg-linux-amd64` | `e62198eb19b136b88c330af83647b5a962cb99b6b1f066758568f12de1974849` |
| linux/arm64 | `rg-linux-arm64` | `c14cdb389f34e504d69e386cfc67d5c5d9a730a990de03ca6910b2a15e30386a` |
| darwin/amd64 | `rg-darwin-amd64` | `0c9a0066db0d26b640777db88045b0ccdd58509a746700e43e1c4ff8707a5ed0` |
| darwin/arm64 | `rg-darwin-arm64` | `a326a1fb48074202e9ad41e4cd1e389eeea372c8c6f7d7e80da81176d5d9430e` |
| windows/amd64 | `rg-windows-amd64.exe` | `14231169855ec5205cf5a1b6f1db358ff4aed4247c86b69ce8aae647c77f6680` |
| windows/arm64 | `rg-windows-arm64.exe` | `d33a29a9ef03c9f4c03be9e8d88498e6e2d2e566d64cdbdef97f9afc8f13120c` |

## fd 10.3.0

From https://github.com/sharkdp/fd/releases/tag/v10.3.0. License texts are
`fd-LICENSE-MIT` and `fd-LICENSE-APACHE` in this directory. Version 10.3.0 is
pinned because it is the only release that ships both `x86_64-apple-darwin`
(removed in 10.4.1) and `aarch64-pc-windows-msvc` (added in 10.3.0), covering
the same six targets as the embedded ripgrep.

Linux artifacts are the static musl builds.

| Target | File | SHA-256 |
|---|---|---|
| linux/amd64 | `fd-linux-amd64` | `9f48273b6c780a5f4f084ef30bc67d98cbd7d10c55c4605cf3a6ee29b741af87` |
| linux/arm64 | `fd-linux-arm64` | `d0fc407937b8a8aec44f3a80b4a08219ba61dde234590358abb168b44478d493` |
| darwin/amd64 | `fd-darwin-amd64` | `e3936d70c47bf8439797aa2c6c1ddff868424ff6bc418fc8501e819a2d58ccad` |
| darwin/arm64 | `fd-darwin-arm64` | `14134aadba85ab2cfe4494d0f44253145f897b77a23fd4d7df2cb4929b53c786` |
| windows/amd64 | `fd-windows-amd64.exe` | `fd3d4853da7a319a604e1cb03ede88cbf584edd12b89a0991871fb4d9cd3ba5b` |
| windows/arm64 | `fd-windows-arm64.exe` | `3d94e5c3f04f1dc4fc0f9836cabf9a087f7b712299fcf1751fdcd055cef408d3` |
