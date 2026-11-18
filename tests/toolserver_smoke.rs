//! End-to-end MCP smoke test for native and upstream tools.
//!
//! Needs the local stack running (devo dev).
//! Run with: cargo test --test toolserver_smoke -- --nocapture
//!
//! Grafana itself need not be running. The Grafana MCP provider is bundled in
//! the toolserver container; day.get is a native in-process tool.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::thread;
use std::time::Duration;

const ADDR: &str = "127.0.0.1:8000";
const HOST: &str = "127.0.0.1:8000";

fn request(method: &str, path: &str, body: &str) -> Result<String, String> {
    let mut stream = TcpStream::connect(ADDR)
        .map_err(|e| format!("could not connect to {ADDR}: {e}"))?;
    stream.set_read_timeout(Some(Duration::from_secs(5))).map_err(|e| e.to_string())?;
    stream.set_write_timeout(Some(Duration::from_secs(5))).map_err(|e| e.to_string())?;

    let mut headers = format!("{method} {path} HTTP/1.0\r\nHost: {HOST}\r\nConnection: close\r\n");
    if method == "POST" {
        headers.push_str(&format!(
            "Content-Type: application/json\r\nAccept: application/json, text/event-stream\r\nMcp-Protocol-Version: 2025-11-25\r\nContent-Length: {}\r\n",
            body.as_bytes().len()
        ));
    }
    headers.push_str("\r\n");

    stream.write_all(headers.as_bytes())
        .and_then(|_| stream.write_all(body.as_bytes()))
        .map_err(|e| e.to_string())?;

    let mut response = Vec::new();
    stream.read_to_end(&mut response).map_err(|e| format!("reading HTTP response: {e}"))?;
    let response = String::from_utf8_lossy(&response);
    let (status_line, rest) = response.split_once("\r\n")
        .ok_or_else(|| format!("invalid HTTP response: {response}"))?;
    let status = status_line.split_whitespace().nth(1).unwrap_or("missing");
    if status != "200" {
        return Err(format!("HTTP {status}: {response}"));
    }
    let (_, response_body) = rest.split_once("\r\n\r\n")
        .ok_or_else(|| format!("HTTP response has no header terminator: {response}"))?;
    Ok(response_body.to_string())
}

fn wait_for_health() -> Result<(), String> {
    let mut last = String::new();
    for _ in 0..30 {
        match request("GET", "/healthz", "") {
            Ok(_) => return Ok(()),
            Err(error) => last = error,
        }
        thread::sleep(Duration::from_secs(1));
    }
    Err(format!("toolserver did not become healthy after 30s: {last}\nIs the stack up? Run devo dev."))
}

#[test]
fn toolserver_exposes_native_and_grafana_tools_over_mcp() {
    wait_for_health().unwrap_or_else(|error| panic!("{error}"));

    let initialize = r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"devo-smoke","version":"0.1.0"}}}"#;
    let response = request("POST", "/mcp", initialize)
        .unwrap_or_else(|error| panic!("MCP initialize failed: {error}"));
    assert!(response.contains("2025-11-25"), "initialize did not negotiate MCP protocol version: {response}");

    let list_tools = r#"{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}"#;
    let response = request("POST", "/mcp", list_tools)
        .unwrap_or_else(|error| panic!("tools/list failed: {error}"));
    assert!(response.contains("day.get"), "catalog omitted native day.get: {response}");
    assert!(response.contains("grafana."), "catalog omitted tools from the bundled Grafana MCP provider: {response}");

    let call_day = r#"{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"day.get","arguments":{"timezone":"America/New_York"}}}"#;
    let response = request("POST", "/mcp", call_day)
        .unwrap_or_else(|error| panic!("day.get failed: {error}"));
    for expected in ["America/New_York", "date", "weekday", "tomorrow", "yesterday"] {
        assert!(response.contains(expected), "day.get response omitted {expected:?}: {response}");
    }
    println!("ok: MCP initializes, lists native and Grafana tools, and invokes day.get in the caller timezone");
}
