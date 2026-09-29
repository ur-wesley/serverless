//! ABI round-trip: POST /invoke through [`oort_sdk::router`] over real TCP.

use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine as _;
use oort_sdk::{router, Ctx, Event, Response};
use std::collections::HashMap;

async fn spawn<H, Fut>(handler: H) -> String
where
    H: Fn(Ctx, Event) -> Fut + Clone + Send + Sync + 'static,
    Fut: std::future::Future<Output = Response> + Send + 'static,
{
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, router(handler)).await.unwrap();
    });
    format!("http://{addr}")
}

#[tokio::test]
async fn healthz_ok() {
    let base = spawn(|_: Ctx, _: Event| async { Response::ok("x") }).await;
    let res = reqwest::get(format!("{base}/healthz")).await.unwrap();
    assert_eq!(res.status(), 200);
    assert_eq!(res.text().await.unwrap(), "ok");
}

#[tokio::test]
async fn invoke_round_trip() {
    let base = spawn(|ctx: Ctx, event: Event| async move {
        assert_eq!(ctx.function_name, "f");
        assert_eq!(event.body, b"ping");
        let mut headers = HashMap::new();
        headers.insert("x-a".to_string(), "b".to_string());
        Response {
            status: 201,
            headers,
            body: b"pong".to_vec(),
        }
    })
    .await;

    let payload = serde_json::json!({
        "event": {
            "method": "GET", "path": "/x",
            "headers": {}, "query": {},
            "body": B64.encode("ping"),
        },
        "ctx": {
            "function_name": "f", "version": "v1",
            "request_id": "r", "deadline_ms": 1,
        },
    });
    let res = reqwest::Client::new()
        .post(format!("{base}/invoke"))
        .json(&payload)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);
    let out: serde_json::Value = res.json().await.unwrap();
    assert_eq!(out["status"], 201);
    assert_eq!(out["headers"]["x-a"], "b");
    let raw = B64.decode(out["body"].as_str().unwrap()).unwrap();
    assert_eq!(raw, b"pong");
}

#[tokio::test]
async fn invoke_bad_json_gives_inner_400() {
    let base = spawn(|_: Ctx, _: Event| async { Response::ok("unreached") }).await;
    let res = reqwest::Client::new()
        .post(format!("{base}/invoke"))
        .header("content-type", "application/json")
        .body("{nope")
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);
    let out: serde_json::Value = res.json().await.unwrap();
    assert_eq!(out["status"], 400);
}
