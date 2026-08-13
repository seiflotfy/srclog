// Exercises the Rust tracing-macro scanner. Parsed by tests, never compiled.
use tracing::{debug, error, info, trace, warn};

fn demo() {
    info!("user logged in");
    error!("dial {}: {}", addr, err);
    warn!(user.id = %id, "slow request {:?}", took);
    tracing::debug!("retrying in {}s", secs);
    info!(target: "net", "connected to {}", host);
    trace!("escaped {{literal}} braces");
    error!(
        "multiline dial {host}: {err}",
    );

    // dynamic: no constant message
    error!("{}", err);
    info!(message_var);

    // not log macros
    let s = r#"raw {} string with info!(fake)"#;
    assert!(cond, "assert is not a log");
    write!(f, "neither is write");
}
