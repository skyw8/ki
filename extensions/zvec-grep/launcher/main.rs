use flate2::read::GzDecoder;
use std::{env, fs, io, path::PathBuf, process::Command};
const RUNTIME: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/runtime.tar.gz"));
const HASH: &str = env!("KI_ZVEC_RUNTIME_HASH");
fn cache_root() -> PathBuf {
    if let Some(home) = env::var_os("KI_HOME") {
        return PathBuf::from(home)
            .join("cache")
            .join("extensions")
            .join("zvec-grep");
    }
    let home = env::var_os("LOCALAPPDATA")
        .or_else(|| env::var_os("XDG_CACHE_HOME"))
        .map(PathBuf::from)
        .or_else(|| {
            env::var_os("HOME")
                .or_else(|| env::var_os("USERPROFILE"))
                .map(|s| PathBuf::from(s).join(".cache"))
        })
        .unwrap_or_else(env::temp_dir);
    home.join("ki").join("extensions").join("zvec-grep")
}
fn run() -> io::Result<()> {
    let cache = cache_root();
    fs::create_dir_all(&cache)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&cache, fs::Permissions::from_mode(0o700))?;
    }
    let directory = cache.join(HASH);
    let executable = if cfg!(windows) {
        "ki-zvec-native.exe"
    } else {
        "ki-zvec-native"
    };
    if !directory.join(".complete").is_file() {
        // Publish complete immutable directories atomically, so concurrent sidecars never load partial libraries.
        let staging = tempfile::Builder::new()
            .prefix("extract-")
            .tempdir_in(&cache)?;
        tar::Archive::new(GzDecoder::new(RUNTIME)).unpack(staging.path())?;
        fs::write(staging.path().join(".complete"), HASH)?;
        if let Err(error) = fs::rename(staging.path(), &directory) {
            if !directory.join(".complete").is_file() {
                return Err(error);
            }
        }
    }
    let mut child = Command::new(directory.join(executable));
    let launcher = env::current_exe()?;
    let alias = env::args_os()
        .next()
        .and_then(|arg| PathBuf::from(arg).file_stem().map(|s| s == "zg"))
        .unwrap_or(false);
    if alias {
        child.arg("--ki-cli");
        if env::args_os().len() == 1 {
            child.arg("--help");
        }
    }
    child
        .args(env::args_os().skip(1))
        .env("KI_ZVEC_GREP_LAUNCHER", launcher);
    #[cfg(target_os = "linux")]
    {
        let paths = std::iter::once(directory.clone())
            .chain(
                env::var_os("LD_LIBRARY_PATH")
                    .map(|s| env::split_paths(&s).collect::<Vec<_>>())
                    .unwrap_or_default(),
            )
            .collect::<Vec<_>>();
        child.env(
            "LD_LIBRARY_PATH",
            env::join_paths(paths).map_err(io::Error::other)?,
        );
    }
    #[cfg(target_os = "macos")]
    {
        child.env("DYLD_LIBRARY_PATH", &directory);
        child.env(
            "GGML_METAL_NO_RESIDENCY",
            env::var_os("GGML_METAL_NO_RESIDENCY").unwrap_or_else(|| "1".into()),
        );
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        return Err(child.exec());
    }
    #[cfg(not(unix))]
    {
        let status = child.status()?;
        std::process::exit(status.code().unwrap_or(1));
    }
}
fn main() {
    if let Err(error) = run() {
        eprintln!("zvec-grep: {error}");
        std::process::exit(1);
    }
}
