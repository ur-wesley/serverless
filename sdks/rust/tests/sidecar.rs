//! `Env` against a fake sidecar (mirrors Go's TestEnvKVBlobAgainstSidecar).

use axum::body::Bytes;
use axum::extract::State;
use axum::routing::{post, put};
use axum::{Json, Router};
use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine as _;
use oort_sdk::{Env, MockEnv};
use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

#[derive(Default)]
struct Fake {
    kv: HashMap<(String, String), Vec<u8>>,
    blobs: HashMap<(String, String), Vec<u8>>,
    published: Vec<(String, Vec<u8>)>,
    logs: Vec<String>,
}

type Shared = Arc<Mutex<Fake>>;

#[derive(Clone)]
struct AppState {
    store: Shared,
    host: String,
}

fn state_store(st: &AppState) -> Shared {
    st.store.clone()
}

fn b64(v: &serde_json::Value, key: &str) -> Vec<u8> {
    v.get(key)
        .and_then(|s| s.as_str())
        .map(|s| B64.decode(s).unwrap_or_default())
        .unwrap_or_default()
}

fn str_field(v: &serde_json::Value, key: &str) -> String {
    v.get(key)
        .and_then(|s| s.as_str())
        .unwrap_or_default()
        .into()
}

async fn kv_get(
    State(st): State<AppState>,
    Json(v): Json<serde_json::Value>,
) -> Json<serde_json::Value> {
    let got = state_store(&st)
        .lock()
        .unwrap()
        .kv
        .get(&(str_field(&v, "function_name"), str_field(&v, "key")))
        .cloned();
    match got {
        Some(raw) => Json(serde_json::json!({"value": B64.encode(raw), "found": true})),
        None => Json(serde_json::json!({"value": "", "found": false})),
    }
}

async fn kv_put(
    State(st): State<AppState>,
    Json(v): Json<serde_json::Value>,
) -> Json<serde_json::Value> {
    state_store(&st).lock().unwrap().kv.insert(
        (str_field(&v, "function_name"), str_field(&v, "key")),
        b64(&v, "value"),
    );
    Json(serde_json::json!({}))
}

async fn kv_del(
    State(st): State<AppState>,
    Json(v): Json<serde_json::Value>,
) -> Json<serde_json::Value> {
    state_store(&st)
        .lock()
        .unwrap()
        .kv
        .remove(&(str_field(&v, "function_name"), str_field(&v, "key")));
    Json(serde_json::json!({}))
}

async fn blob_url(
    State(st): State<AppState>,
    axum::extract::Path(op): axum::extract::Path<String>,
    Json(v): Json<serde_json::Value>,
) -> Json<serde_json::Value> {
    let _ = op;
    let host = st.host.clone();
    let key = format!(
        "{}/{}/{}",
        str_field(&v, "function_name"),
        op,
        str_field(&v, "key")
    );
    // Encode target in the URL path so the transfer handlers can route it.
    let url = format!("{host}/xfer/{key}");
    Json(serde_json::json!({"url": url}))
}

async fn xfer_put(
    State(st): State<AppState>,
    axum::extract::Path(path): axum::extract::Path<String>,
    body: Bytes,
) -> Json<serde_json::Value> {
    // path = "<fn>/put-url/<key>" or "<fn>/get-url/<key>"; recover fn+key.
    let mut parts = path.splitn(3, '/');
    let f = parts.next().unwrap_or("").to_string();
    let _op = parts.next().unwrap_or("");
    let k = parts.next().unwrap_or("").to_string();
    state_store(&st)
        .lock()
        .unwrap()
        .blobs
        .insert((f, k), body.to_vec());
    Json(serde_json::json!({}))
}

async fn xfer_get(
    State(st): State<AppState>,
    axum::extract::Path(path): axum::extract::Path<String>,
) -> Vec<u8> {
    let mut parts = path.splitn(3, '/');
    let f = parts.next().unwrap_or("").to_string();
    let _op = parts.next().unwrap_or("");
    let k = parts.next().unwrap_or("").to_string();
    state_store(&st)
        .lock()
        .unwrap()
        .blobs
        .get(&(f, k))
        .cloned()
        .unwrap_or_default()
}

async fn queue_publish(
    State(st): State<AppState>,
    Json(v): Json<serde_json::Value>,
) -> Json<serde_json::Value> {
    state_store(&st)
        .lock()
        .unwrap()
        .published
        .push((str_field(&v, "topic"), b64(&v, "message")));
    Json(serde_json::json!({}))
}

async fn log_append(
    State(st): State<AppState>,
    Json(v): Json<serde_json::Value>,
) -> Json<serde_json::Value> {
    state_store(&st)
        .lock()
        .unwrap()
        .logs
        .push(str_field(&v, "line"));
    Json(serde_json::json!({}))
}

async fn logs_tail(Json(_): Json<serde_json::Value>) -> Json<serde_json::Value> {
    Json(
        serde_json::json!({"lines": [{"Function": "f", "Version": "v1", "RequestID": "r", "Line": "hi", "Time": "t"}]}),
    )
}

async fn functions_list(Json(_): Json<serde_json::Value>) -> Json<serde_json::Value> {
    Json(serde_json::json!({"functions": ["f", "g"]}))
}

async fn invoke_other(Json(_): Json<serde_json::Value>) -> Json<serde_json::Value> {
    Json(
        serde_json::json!({"status": 200, "headers": {"x-echo": "1"}, "body": B64.encode("echo-ok")}),
    )
}

async fn spawn_fake() -> (String, Shared) {
    let store: Shared = Arc::new(Mutex::new(Fake::default()));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let base = format!("http://{addr}");
    let app = Router::new()
        .route("/sidecar/kv/get", post(kv_get))
        .route("/sidecar/kv/put", post(kv_put))
        .route("/sidecar/kv/del", post(kv_del))
        .route("/sidecar/blob/:op", post(blob_url))
        .route("/xfer/*path", put(xfer_put).get(xfer_get))
        .route("/sidecar/queue/publish", post(queue_publish))
        .route("/sidecar/log/append", post(log_append))
        .route("/sidecar/logs/tail", post(logs_tail))
        .route("/sidecar/functions/list", post(functions_list))
        .route("/sidecar/invoke", post(invoke_other))
        .with_state(AppState {
            store: store.clone(),
            host: base.clone(),
        });
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    (base, store)
}

#[tokio::test]
async fn env_kv_blob_queue_log() {
    let (base, st) = spawn_fake().await;
    let env = Env::new(&base);

    assert_eq!(env.kv_get("f", "k").await.unwrap(), None);
    env.kv_put("f", "k", b"v", Duration::from_secs(0))
        .await
        .unwrap();
    assert_eq!(env.kv_get("f", "k").await.unwrap(), Some(b"v".to_vec()));
    env.kv_del("f", "k").await.unwrap();
    assert_eq!(env.kv_get("f", "k").await.unwrap(), None);

    env.blob_put_bytes("f", "a.bin", b"blobdata").await.unwrap();
    assert_eq!(env.blob_get_bytes("f", "a.bin").await.unwrap(), b"blobdata");

    env.queue_publish("t", b"m").await.unwrap();
    env.log("f", "v1", "r", "line 1").await.unwrap();

    let guard = st.lock().unwrap();
    assert_eq!(guard.published, vec![("t".to_string(), b"m".to_vec())]);
    assert_eq!(guard.logs, vec!["line 1".to_string()]);
}

#[tokio::test]
async fn env_intercom() {
    let (base, _st) = spawn_fake().await;
    let env = Env::new(&base);

    let lines = env.logs_tail("f", 10).await.unwrap();
    assert_eq!(lines.len(), 1);
    assert_eq!(lines[0].Line, "hi");

    assert_eq!(env.functions_list().await.unwrap(), vec!["f", "g"]);

    let out = env
        .invoke_other("g", "GET", "/x", &HashMap::new(), &HashMap::new(), b"ping")
        .await
        .unwrap();
    assert_eq!(out.status, 200);
    assert_eq!(out.headers.get("x-echo").map(String::as_str), Some("1"));
    assert_eq!(out.body, b"echo-ok");
}

#[tokio::test]
async fn mock_env_round_trip() {
    let m = MockEnv::new();
    assert_eq!(m.kv_get("f", "k").await.unwrap(), None);
    m.kv_put("f", "k", b"v").await.unwrap();
    assert_eq!(m.kv_get("f", "k").await.unwrap(), Some(b"v".to_vec()));
    m.kv_del("f", "k").await.unwrap();
    assert_eq!(m.kv_get("f", "k").await.unwrap(), None);

    m.blob_put_bytes("f", "a", b"data").await.unwrap();
    assert_eq!(m.blob_get_bytes("f", "a").await.unwrap(), b"data");
    assert!(m.blob_get_bytes("f", "missing").await.is_err());

    m.queue_publish("t", b"m").await.unwrap();
    m.log("hello").await.unwrap();
    assert_eq!(m.published(), vec![("t".to_string(), b"m".to_vec())]);
    assert_eq!(m.logs(), vec!["hello".to_string()]);
}
