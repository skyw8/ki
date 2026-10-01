# Embedded search executables

These platform binaries are unmodified release artifacts. The build embeds only
the artifact selected by the target `GOOS/GOARCH`. At runtime ki materializes
the embedded rg and fd into a shared tools directory and both executes them and
puts that directory on the shell PATH.

The checked-in assets cover only the three supported release targets:
`linux/amd64`, `darwin/arm64`, and `windows/amd64`.

## ripgrep 15.2.0

From https://github.com/BurntSushi/ripgrep/releases/tag/15.2.0. License texts
are `LICENSE-MIT`, `UNLICENSE`, and `COPYING` in this directory.

| Target | File | SHA-256 |
|---|---|---|
| linux/amd64 | `rg-linux-amd64` | `e62198eb19b136b88c330af83647b5a962cb99b6b1f066758568f12de1974849` |
| darwin/arm64 | `rg-darwin-arm64` | `a326a1fb48074202e9ad41e4cd1e389eeea372c8c6f7d7e80da81176d5d9430e` |
| windows/amd64 | `rg-windows-amd64.exe` | `14231169855ec5205cf5a1b6f1db358ff4aed4247c86b69ce8aae647c77f6680` |

## fd 10.5.0

From https://github.com/sharkdp/fd/releases/tag/v10.5.0. License texts are
`fd-LICENSE-MIT` and `fd-LICENSE-APACHE` in this directory. Version 10.5.0
ships both `x86_64-apple-darwin` and
`aarch64-pc-windows-msvc`, while this build embeds only the three supported
release targets above. Upstream notes that releases based on Rust 1.90 no longer fully
support Intel Mac or Windows 7; validate those legacy OSes separately if they
remain in the deployment matrix.

Linux artifacts are the static musl builds.

| Target | File | SHA-256 |
|---|---|---|
| linux/amd64 | `fd-linux-amd64` | `e79642a479d2816c887047476bbe6ad229465f30de031033574dee2a095e0037` |
| darwin/arm64 | `fd-darwin-arm64` | `cf3bde435da174f41cf9589a2efeaf03804df7c250fc15e8b8a1e9bfc66ebc9a` |
| windows/amd64 | `fd-windows-amd64.exe` | `d67d27a8e375ed7e9bca2b506a9dd5082bc24547aeba908b863f6f8b8ab0c3b9` |
