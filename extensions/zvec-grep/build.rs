use flate2::{Compression, write::GzEncoder};
use sha2::{Digest, Sha256};
use std::{
    env,
    fs::{self, File},
    path::{Path, PathBuf},
    process::Command,
};

fn main() {
    for path in [
        "native/src",
        "native/Cargo.toml",
        "native/Cargo.lock",
        "native/build.rs",
        "native/search-tool.json",
        "native/UPSTREAM-LICENSE",
    ] {
        println!("cargo:rerun-if-changed={path}");
    }
    for key in [
        "KI_ZVEC_GREP_BUILD_JOBS",
        "BINDGEN_EXTRA_CLANG_ARGS",
        "ZVEC_LIB_DIR",
        "ORT_LIB_LOCATION",
    ] {
        println!("cargo:rerun-if-env-changed={key}");
    }
    let root = PathBuf::from(env::var_os("CARGO_MANIFEST_DIR").unwrap());
    let target = root.join("native/target");
    let mut build = Command::new("rustup");
    build
        .args([
            "run",
            "1.98.0",
            "cargo",
            "build",
            "--release",
            "--locked",
            "--manifest-path",
        ])
        .arg(root.join("native/Cargo.toml"));
    let jobs = env::var("KI_ZVEC_GREP_BUILD_JOBS").unwrap_or("4".into());
    if env::var_os("BINDGEN_EXTRA_CLANG_ARGS").is_none()
        && env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("linux")
    {
        // Some minimal Linux SDKs ship libclang without Clang's standard C headers.
        // GCC's include directory supplies stdbool/stddef without hardcoding a host path.
        if let Ok(output) = Command::new("cc").arg("-print-file-name=include").output() {
            let include = String::from_utf8_lossy(&output.stdout).trim().to_owned();
            if output.status.success() && Path::new(&include).is_dir() {
                build.env("BINDGEN_EXTRA_CLANG_ARGS", format!("-I{include}"));
            }
        }
    }
    build
        .args(["-j", &jobs])
        .env("CARGO_TARGET_DIR", &target)
        .env("CMAKE_BUILD_PARALLEL_LEVEL", &jobs);
    // Cargo's outer compiler settings must not point the inner build at the launcher's compiler.
    build
        .env_remove("RUSTC")
        .env_remove("RUSTDOC")
        .env_remove("CARGO_ENCODED_RUSTFLAGS");
    let triple = env::var("TARGET").unwrap();
    let host = env::var("HOST").unwrap();
    let directory = if triple != host {
        build.args(["--target", &triple]);
        target.join(&triple).join("release")
    } else {
        target.join("release")
    };
    let status=build.status().expect("build the native zvec-grep engine (requires Rust 1.98, CMake, a C++ compiler and libclang)");
    assert!(status.success(), "native zvec-grep engine build failed");
    let executable = if triple.contains("windows") {
        "ki-zvec-native.exe"
    } else {
        "ki-zvec-native"
    };
    let out = PathBuf::from(env::var_os("OUT_DIR").unwrap());
    let archive = out.join("runtime.tar.gz");
    let compressed = GzEncoder::new(File::create(&archive).unwrap(), Compression::default());
    let mut tar = tar::Builder::new(compressed);
    tar.follow_symlinks(true);
    tar.append_path_with_name(directory.join(executable), executable)
        .unwrap();
    tar.append_path_with_name(
        root.join("native/UPSTREAM-LICENSE"),
        "licenses/zvec-grep-APACHE-2.0.txt",
    )
    .unwrap();
    let mut libraries = 0;
    for entry in fs::read_dir(&directory).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if name.contains(".so") || name.ends_with(".dylib") || name.ends_with(".dll") {
            tar.append_path_with_name(entry.path(), Path::new(name.as_ref()))
                .unwrap();
            libraries += 1;
        }
    }
    assert!(
        libraries >= 1,
        "native zvec shared library must be bundled (ONNX is statically linked when supplied that way)"
    );
    for name in ["jieba.dict.utf8", "hmm_model.utf8"] {
        let relative = Path::new("data").join("jieba_dict").join(name);
        assert!(
            directory.join(&relative).is_file(),
            "missing Jieba tokenizer resource {name}"
        );
        tar.append_path_with_name(directory.join(&relative), relative)
            .unwrap();
    }
    tar.into_inner().unwrap().finish().unwrap();
    let digest = Sha256::digest(fs::read(&archive).unwrap());
    println!("cargo:rustc-env=KI_ZVEC_RUNTIME_HASH={digest:x}");
}
