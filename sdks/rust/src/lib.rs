//! Rust SDK for Oort: handler ABI server plus sidecar `Env` client.
//!
//! Any language works raw (`GET /healthz`, `POST /invoke` with base64
//! bodies), but this crate wraps it:
//!
//! ```no_run
//! use oort_sdk::{serve, Ctx, Event, Response};
//!
//! #[tokio::main]
//! async fn main() {
//!     serve(|ctx: Ctx, event: Event| async move {
//!         let env = oort_sdk::Env::from_env();
//!         let _ = env.log(&ctx.function_name, &ctx.version, &ctx.request_id, "hello").await;
//!         Response::ok("ok")
//!     })
//!     .await;
//! }
//! ```
//!
//! [`Env::from_env`] reads `ACTIONS_SIDECAR_URL` / `ACTIONS_SIDECAR_TOKEN`
//! (auto-injected; the URL is also on [`Ctx::sidecar_url`]).
//! [`MockEnv`] covers `oort dev --offline`.

use std::collections::HashMap;
use std::fmt;
use std::future::Future;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::body::Bytes;
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Json, Router};
use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine as _;
use serde::{Deserialize, Serialize};

/// Incoming request event (decoded from base64 wire form).
#[derive(Debug, Clone, Default)]
pub struct Event {
    pub method: String,
    pub path: String,
    pub headers: HashMap<String, String>,
    pub query: HashMap<String, String>,
    pub body: Vec<u8>,
}

/// Invoke context: which function/version/request this is.
#[derive(Debug, Clone, Default)]
pub struct Ctx {
    pub function_name: String,
    pub version: String,
    pub request_id: String,
    pub deadline_ms: i64,
    pub sidecar_url: String,
}

/// Handler response. `status` defaults to 200 when zero.
#[derive(Debug, Clone, Default)]
pub struct Response {
    pub status: u16,
    pub headers: HashMap<String, String>,
    pub body: Vec<u8>,
}

impl Response {
    pub fn ok(body: impl Into<Vec<u8>>) -> Self {
        Response {
            status: 200,
            body: body.into(),
            ..Default::default()
        }
    }

    pub fn status(status: u16, body: impl Into<Vec<u8>>) -> Self {
        Response {
            status,
            body: body.into(),
            ..Default::default()
        }
    }
}

// --- Wire format (protojson-ish; bytes fields are base64) ---

#[derive(Deserialize, Default)]
struct WireEvent {
    #[serde(default)]
    method: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    headers: HashMap<String, String>,
    #[serde(default)]
    query: HashMap<String, String>,
    #[serde(default)]
    body: String,
}

#[derive(Deserialize, Default)]
struct WireCtx {
    #[serde(default)]
    function_name: String,
    #[serde(default)]
    version: String,
    #[serde(default)]
    request_id: String,
    #[serde(default)]
    deadline_ms: i64,
    #[serde(default)]
    sidecar_url: String,
}

#[derive(Deserialize)]
struct InvokeRequest {
    #[serde(default)]
    event: WireEvent,
    #[serde(default)]
    ctx: WireCtx,
}

#[derive(Serialize)]
struct InvokeResponse {
    status: u16,
    headers: HashMap<String, String>,
    body: String,
}

/// Max invoke JSON body, mirroring the Go SDK (8 MiB).
pub const MAX_INVOKE_BYTES: usize = 8 << 20;

fn decode_body(b64: &str) -> Vec<u8> {
    B64.decode(b64).unwrap_or_default()
}

fn encode_body(raw: &[u8]) -> String {
    B64.encode(raw)
}

fn respond(resp: Response) -> impl IntoResponse {
    let status = if resp.status == 0 { 200 } else { resp.status };
    Json(InvokeResponse {
        status,
        headers: resp.headers,
        body: encode_body(&resp.body),
    })
}

async fn healthz() -> &'static str {
    "ok"
}

#[derive(Clone)]
struct HandlerState<H> {
    handler: H,
}

async fn invoke<H, Fut>(
    axum::extract::State(state): axum::extract::State<HandlerState<H>>,
    body: Bytes,
) -> impl IntoResponse
where
    H: Fn(Ctx, Event) -> Fut + Clone + Send + Sync + 'static,
    Fut: Future<Output = Response> + Send + 'static,
{
    if body.len() > MAX_INVOKE_BYTES {
        return respond(Response::status(400, "invoke too large"));
    }
    let parsed: InvokeRequest = match serde_json::from_slice(&body) {
        Ok(v) => v,
        Err(_) => return respond(Response::status(400, "bad invoke JSON")),
    };
    let ctx = Ctx {
        function_name: parsed.ctx.function_name,
        version: parsed.ctx.version,
        request_id: parsed.ctx.request_id,
        deadline_ms: parsed.ctx.deadline_ms,
        sidecar_url: parsed.ctx.sidecar_url,
    };
    let event = Event {
        method: parsed.event.method,
        path: parsed.event.path,
        headers: parsed.event.headers,
        query: parsed.event.query,
        body: decode_body(&parsed.event.body),
    };
    let out = (state.handler)(ctx, event).await;
    respond(out)
}

/// Build the ABI router (`GET /healthz`, `POST /invoke`) around `handler`.
/// Use it to embed the handler or drive it in tests.
pub fn router<H, Fut>(handler: H) -> Router
where
    H: Fn(Ctx, Event) -> Fut + Clone + Send + Sync + 'static,
    Fut: Future<Output = Response> + Send + 'static,
{
    Router::new()
        .route("/healthz", get(healthz))
        .route("/invoke", post(invoke::<H, Fut>))
        .with_state(HandlerState { handler })
}

/// Serve `handler` on `$PORT` (default 3000).
pub async fn serve<H, Fut>(handler: H)
where
    H: Fn(Ctx, Event) -> Fut + Clone + Send + Sync + 'static,
    Fut: Future<Output = Response> + Send + 'static,
{
    let port: u16 = std::env::var("PORT")
        .ok()
        .and_then(|p| p.parse().ok())
        .unwrap_or(3000);
    let addr = format!("0.0.0.0:{port}");
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .expect("bind handler port");
    println!("handler listening on {addr}");
    axum::serve(listener, router(handler))
        .await
        .expect("serve handler");
}

// --- Errors ---

/// SDK error: transport failure, bad payload, or sidecar rejection.
#[derive(Debug)]
pub enum Error {
    /// HTTP transport failure.
    Http(reqwest::Error),
    /// Sidecar answered non-200: `"<path>: <status>: <body>"`.
    Api(String),
    /// Response payload was not valid base64.
    Base64(base64::DecodeError),
    /// Blob download exceeded the 64 MiB cap, or offline mock misses.
    Other(String),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Http(e) => write!(f, "http: {e}"),
            Error::Api(s) => write!(f, "{s}"),
            Error::Base64(e) => write!(f, "base64: {e}"),
            Error::Other(s) => write!(f, "{s}"),
        }
    }
}

impl std::error::Error for Error {}

impl From<reqwest::Error> for Error {
    fn from(e: reqwest::Error) -> Self {
        Error::Http(e)
    }
}

impl From<base64::DecodeError> for Error {
    fn from(e: base64::DecodeError) -> Self {
        Error::Base64(e)
    }
}

pub type Result<T> = std::result::Result<T, Error>;

// --- Sidecar client ---

/// Sidecar `Env` client. Construct with [`Env::from_env`].
#[derive(Clone)]
pub struct Env {
    base_url: String,
    token: String,
    client: reqwest::Client,
}

impl Env {
    /// Build from `ACTIONS_SIDECAR_URL` / `ACTIONS_SIDECAR_TOKEN`.
    pub fn from_env() -> Self {
        Env {
            base_url: std::env::var("ACTIONS_SIDECAR_URL").unwrap_or_default(),
            token: std::env::var("ACTIONS_SIDECAR_TOKEN").unwrap_or_default(),
            client: reqwest::Client::new(),
        }
    }

    /// Build for an explicit base URL (token still comes from the env).
    /// Useful in tests.
    pub fn new(base_url: impl Into<String>) -> Self {
        Env {
            base_url: base_url.into(),
            token: std::env::var("ACTIONS_SIDECAR_TOKEN").unwrap_or_default(),
            client: reqwest::Client::new(),
        }
    }

    async fn post<T: serde::de::DeserializeOwned>(
        &self,
        path: &str,
        body: serde_json::Value,
    ) -> Result<T> {
        let mut req = self
            .client
            .post(format!("{}{path}", self.base_url))
            .json(&body);
        if !self.token.is_empty() {
            req = req.header("X-Actions-Token", &self.token);
        }
        let resp = req.send().await?;
        if resp.status() != StatusCode::OK {
            let status = resp.status();
            let raw = resp.text().await.unwrap_or_default();
            let capped: String = raw.chars().take(1024).collect();
            return Err(Error::Api(format!("{path}: {status}: {capped}")));
        }
        Ok(resp.json().await?)
    }

    async fn post_unit(&self, path: &str, body: serde_json::Value) -> Result<()> {
        let v: serde_json::Value = self.post(path, body).await?;
        let _ = v;
        Ok(())
    }

    pub async fn kv_get(&self, function: &str, key: &str) -> Result<Option<Vec<u8>>> {
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            value: String,
            #[serde(default)]
            found: bool,
        }
        let out: Out = self
            .post(
                "/sidecar/kv/get",
                serde_json::json!({"function_name": function, "key": key}),
            )
            .await?;
        if !out.found {
            return Ok(None);
        }
        Ok(Some(B64.decode(out.value)?))
    }

    pub async fn kv_put(
        &self,
        function: &str,
        key: &str,
        value: &[u8],
        ttl: Duration,
    ) -> Result<()> {
        self.post_unit(
            "/sidecar/kv/put",
            serde_json::json!({
                "function_name": function,
                "key": key,
                "value": B64.encode(value),
                "ttl_seconds": ttl.as_secs() as i64,
            }),
        )
        .await
    }

    pub async fn kv_del(&self, function: &str, key: &str) -> Result<()> {
        self.post_unit(
            "/sidecar/kv/del",
            serde_json::json!({"function_name": function, "key": key}),
        )
        .await
    }

    async fn blob_url(&self, op: &str, function: &str, key: &str) -> Result<String> {
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            url: String,
        }
        let out: Out = self
            .post(
                &format!("/sidecar/blob/{op}"),
                serde_json::json!({"function_name": function, "key": key}),
            )
            .await?;
        Ok(out.url)
    }

    /// Upload via presigned PUT (S3) or the local transfer endpoint.
    pub async fn blob_put_bytes(&self, function: &str, key: &str, data: &[u8]) -> Result<()> {
        let url = self.blob_url("put-url", function, key).await?;
        let mut req = self.client.put(url).body(data.to_vec());
        if !self.token.is_empty() {
            req = req.header("X-Actions-Token", &self.token);
        }
        let resp = req.send().await?;
        if resp.status().as_u16() >= 300 {
            return Err(Error::Api(format!("blob put: {}", resp.status())));
        }
        Ok(())
    }

    /// Download via presigned GET (S3) or the local transfer endpoint.
    /// Caps at 64 MiB like the Go SDK.
    pub async fn blob_get_bytes(&self, function: &str, key: &str) -> Result<Vec<u8>> {
        let url = self.blob_url("get-url", function, key).await?;
        let mut req = self.client.get(url);
        if !self.token.is_empty() {
            req = req.header("X-Actions-Token", &self.token);
        }
        let resp = req.send().await?;
        if resp.status() != StatusCode::OK {
            return Err(Error::Api(format!("blob get: {}", resp.status())));
        }
        let bytes = resp.bytes().await?;
        if bytes.len() > 64 << 20 {
            return Err(Error::Other("blob exceeds 64 MiB cap".into()));
        }
        Ok(bytes.to_vec())
    }

    pub async fn queue_publish(&self, topic: &str, message: &[u8]) -> Result<()> {
        self.post_unit(
            "/sidecar/queue/publish",
            serde_json::json!({"topic": topic, "message": B64.encode(message)}),
        )
        .await
    }

    pub async fn log(
        &self,
        function: &str,
        version: &str,
        request_id: &str,
        line: &str,
    ) -> Result<()> {
        self.post_unit(
            "/sidecar/log/append",
            serde_json::json!({
                "function_name": function,
                "version": version,
                "request_id": request_id,
                "line": line,
            }),
        )
        .await
    }

    /// Read another function's logs via intercom (allow_logs + same owner).
    pub async fn logs_tail(&self, target: &str, limit: i64) -> Result<Vec<LogLine>> {
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            lines: Vec<LogLine>,
        }
        let out: Out = self
            .post(
                "/sidecar/logs/tail",
                serde_json::json!({"target_function": target, "limit": limit}),
            )
            .await?;
        Ok(out.lines)
    }

    /// Own function names for dashboard discovery.
    pub async fn functions_list(&self) -> Result<Vec<String>> {
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            functions: Vec<String>,
        }
        let out: Out = self
            .post("/sidecar/functions/list", serde_json::json!({}))
            .await?;
        Ok(out.functions)
    }

    /// Call another function via intercom (allow_invoke + same owner).
    #[allow(clippy::too_many_arguments)]
    pub async fn invoke_other(
        &self,
        target: &str,
        method: &str,
        path: &str,
        headers: &HashMap<String, String>,
        query: &HashMap<String, String>,
        body: &[u8],
    ) -> Result<InvokeResult> {
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            status: u16,
            #[serde(default)]
            headers: HashMap<String, String>,
            #[serde(default)]
            body: String,
        }
        let out: Out = self
            .post(
                "/sidecar/invoke",
                serde_json::json!({
                    "target_function": target,
                    "method": method,
                    "path": path,
                    "headers": headers,
                    "query": query,
                    "body": B64.encode(body),
                }),
            )
            .await?;
        Ok(InvokeResult {
            status: out.status,
            headers: out.headers,
            body: B64.decode(out.body)?,
        })
    }
}

/// One log line, mirroring the sidecar log ring.
// Capitalized fields match the Go sidecar's JSON keys wire-compatibly.
#[derive(Debug, Clone, Deserialize)]
#[allow(non_snake_case)]
pub struct LogLine {
    #[serde(default)]
    pub Function: String,
    #[serde(default)]
    pub Version: String,
    #[serde(default)]
    pub RequestID: String,
    #[serde(default)]
    pub Line: String,
    #[serde(default)]
    pub Time: String,
}

/// Result of [`Env::invoke_other`].
#[derive(Debug, Clone)]
pub struct InvokeResult {
    pub status: u16,
    pub headers: HashMap<String, String>,
    pub body: Vec<u8>,
}

// --- Offline mock (oort dev --offline) ---

#[derive(Debug, Default)]
struct MockState {
    kv: HashMap<(String, String), Vec<u8>>,
    blobs: HashMap<(String, String), Vec<u8>>,
    published: Vec<(String, Vec<u8>)>,
    logs: Vec<String>,
}

/// In-memory mock of the sidecar `Env` surface for offline dev.
/// Mirrors the TS `mockEnv()`.
#[derive(Clone, Default)]
pub struct MockEnv {
    inner: Arc<Mutex<MockState>>,
}

impl MockEnv {
    pub fn new() -> Self {
        MockEnv::default()
    }

    pub async fn kv_get(&self, function: &str, key: &str) -> Result<Option<Vec<u8>>> {
        Ok(self
            .inner
            .lock()
            .expect("mock lock")
            .kv
            .get(&(function.to_string(), key.to_string()))
            .cloned())
    }

    pub async fn kv_put(&self, function: &str, key: &str, value: &[u8]) -> Result<()> {
        self.inner
            .lock()
            .expect("mock lock")
            .kv
            .insert((function.to_string(), key.to_string()), value.to_vec());
        Ok(())
    }

    pub async fn kv_del(&self, function: &str, key: &str) -> Result<()> {
        self.inner
            .lock()
            .expect("mock lock")
            .kv
            .remove(&(function.to_string(), key.to_string()));
        Ok(())
    }

    pub async fn blob_put_bytes(&self, function: &str, key: &str, data: &[u8]) -> Result<()> {
        self.inner
            .lock()
            .expect("mock lock")
            .blobs
            .insert((function.to_string(), key.to_string()), data.to_vec());
        Ok(())
    }

    pub async fn blob_get_bytes(&self, function: &str, key: &str) -> Result<Vec<u8>> {
        self.inner
            .lock()
            .expect("mock lock")
            .blobs
            .get(&(function.to_string(), key.to_string()))
            .cloned()
            .ok_or_else(|| Error::Other("not found".into()))
    }

    pub async fn queue_publish(&self, topic: &str, message: &[u8]) -> Result<()> {
        self.inner
            .lock()
            .expect("mock lock")
            .published
            .push((topic.to_string(), message.to_vec()));
        Ok(())
    }

    pub async fn log(&self, line: &str) -> Result<()> {
        self.inner
            .lock()
            .expect("mock lock")
            .logs
            .push(line.to_string());
        Ok(())
    }

    /// Snapshot of published `(topic, message)` pairs (test helper).
    pub fn published(&self) -> Vec<(String, Vec<u8>)> {
        self.inner.lock().expect("mock lock").published.clone()
    }

    /// Snapshot of logged lines (test helper).
    pub fn logs(&self) -> Vec<String> {
        self.inner.lock().expect("mock lock").logs.clone()
    }
}
