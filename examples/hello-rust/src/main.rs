// hello-rust: minimal ABI handler using oort-sdk.
// Speaks GET /healthz + POST /invoke on $PORT (default 3000).
use oort_sdk::{serve, Ctx, Env, Event, Response};
use std::collections::HashMap;

#[tokio::main]
async fn main() {
    serve(|ctx: Ctx, event: Event| async move {
        let body = String::from_utf8_lossy(&event.body);
        let text = format!(
            "hello from hello-rust {} {} body={}",
            event.method, event.path, body
        );
        // Fire-and-forget log so the ui dashboard logs tab has content.
        let env = Env::from_env();
        let line = format!(
            "http {} {} body={} bytes",
            event.method,
            event.path,
            event.body.len()
        );
        let _ = env
            .log(&ctx.function_name, &ctx.version, &ctx.request_id, &line)
            .await;
        let mut headers = HashMap::new();
        headers.insert("content-type".to_string(), "text/plain".to_string());
        Response {
            status: 200,
            headers,
            body: text.into_bytes(),
        }
    })
    .await;
}
