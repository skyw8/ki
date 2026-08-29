mod config;
mod discovery;
mod event;
mod http;
mod ir;
mod openrouter;
mod race;
mod router;
mod sidecar;

use clap::{Parser, Subcommand};
use race::Pool;
use std::sync::Arc;

#[derive(Parser, Debug)]
#[command(name = "freerouter", about = "OpenRouter free-model race router")]
struct Cli {
    #[command(subcommand)]
    command: Option<Commands>,

    /// HTTP listen address (default 127.0.0.1:18427)
    #[arg(long, env = "FREEROUTER_LISTEN")]
    listen: Option<String>,

    /// OpenRouter API base URL
    #[arg(long, env = "OPENROUTER_BASE_URL")]
    base_url: Option<String>,

    /// Verbose logging
    #[arg(long, short)]
    verbose: bool,
}

#[derive(Subcommand, Debug)]
enum Commands {
    /// Run as ki extension sidecar (HTTP + NDJSON JSON-RPC on stdin/stdout)
    Sidecar,
    /// Run standalone HTTP proxy only (default when no subcommand)
    Serve,
}

#[tokio::main]
async fn main() {
    let cli = Cli::parse();
    if cli.verbose {
        let _ = tracing_subscriber::fmt()
            .with_env_filter("freerouter=debug")
            .with_writer(std::io::stderr)
            .try_init();
    }

    let is_sidecar = matches!(cli.command, Some(Commands::Sidecar));

    let client = reqwest::Client::builder()
        .user_agent("freerouter/0.1")
        .build()
        .expect("reqwest client");

    if is_sidecar {
        let root = sidecar::extension_root_from_env();
        let cfg = config::load_sidecar(&root);
        let listen = cfg.listen.clone();
        let pool = Pool::new(cfg, client);
        let pool_http = Arc::clone(&pool);
        let http_task = tokio::spawn(async move {
            if let Err(err) = http::serve(pool_http, &listen).await {
                eprintln!("[freerouter] {err}");
                std::process::exit(1);
            }
        });
        if let Err(err) = sidecar::run_sidecar(pool, root).await {
            eprintln!("[freerouter] sidecar error: {err}");
            std::process::exit(1);
        }
        http_task.abort();
        return;
    }

    // Standalone serve (default)
    let cfg = config::load_standalone(cli.listen.clone(), cli.base_url.clone());
    if cfg.api_key.is_empty() {
        eprintln!(
            "[freerouter] OPENROUTER_API_KEY (or FREEROUTER_API_KEY) is required for standalone mode"
        );
        std::process::exit(1);
    }
    let listen = cfg.listen.clone();
    let pool = Pool::new(cfg, client);
    if let Err(err) = http::serve(pool, &listen).await {
        eprintln!("[freerouter] {err}");
        std::process::exit(1);
    }
}
