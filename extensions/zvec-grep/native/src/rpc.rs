use serde_json::{Value, json};
use std::{
    collections::{HashMap, HashSet},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
};
use tokio::{
    io::{AsyncWriteExt, BufWriter, Stdout},
    sync::{mpsc, oneshot},
};

enum Output {
    Message(Value),
    Flush(oneshot::Sender<()>),
}
pub struct Rpc {
    send: mpsc::UnboundedSender<Output>,
    pending: Mutex<HashMap<String, oneshot::Sender<Result<Value, String>>>>,
    answered: Mutex<HashSet<String>>,
    sequence: AtomicU64,
    closed: AtomicBool,
}
impl Rpc {
    pub fn new() -> Arc<Self> {
        let (send, mut recv) = mpsc::unbounded_channel::<Output>();
        tokio::spawn(async move {
            let mut output: BufWriter<Stdout> = BufWriter::new(tokio::io::stdout());
            while let Some(message) = recv.recv().await {
                let value = match message {
                    Output::Message(value) => value,
                    Output::Flush(done) => {
                        let _ = output.flush().await;
                        let _ = done.send(());
                        continue;
                    }
                };
                let mut line = serde_json::to_vec(&value).unwrap();
                line.push(b'\n');
                if output.write_all(&line).await.is_err() || output.flush().await.is_err() {
                    break;
                }
            }
        });
        Arc::new(Self {
            send,
            pending: Mutex::new(HashMap::new()),
            answered: Mutex::new(HashSet::new()),
            sequence: AtomicU64::new(1),
            closed: AtomicBool::new(false),
        })
    }
    pub fn key(id: &Value) -> String {
        id.as_str()
            .map(str::to_owned)
            .unwrap_or_else(|| id.to_string())
    }
    pub fn send(&self, value: Value) {
        let _ = self.send.send(Output::Message(value));
    }
    pub fn is_closed(&self) -> bool {
        self.closed.load(Ordering::Acquire)
    }
    pub fn close_input(&self) {
        let mut pending = self.pending.lock().unwrap();
        self.closed.store(true, Ordering::Release);
        pending.clear();
    }
    pub async fn flush(&self) {
        let (send, recv) = oneshot::channel();
        if self.send.send(Output::Flush(send)).is_ok() {
            let _ = recv.await;
        }
    }
    pub async fn call(&self, method: &str, params: Value) -> Result<Value, String> {
        let id = format!("z{}", self.sequence.fetch_add(1, Ordering::Relaxed));
        let (send, recv) = oneshot::channel();
        {
            let mut pending = self.pending.lock().unwrap();
            if self.is_closed() {
                return Err("rpc closed".into());
            }
            pending.insert(id.clone(), send);
        }
        self.send(json!({"jsonrpc":"2.0","id":id,"method":method,"params":params}));
        recv.await.map_err(|_| "rpc closed".to_owned())?
    }
    pub fn receive(&self, value: Value) {
        if let Some(send) = self
            .pending
            .lock()
            .unwrap()
            .remove(&Self::key(&value["id"]))
        {
            let result = if value.get("error").is_some() {
                Err(value["error"]["message"]
                    .as_str()
                    .unwrap_or("rpc error")
                    .to_owned())
            } else {
                Ok(value["result"].clone())
            };
            let _ = send.send(result);
        }
    }
    pub fn reply(&self, id: Value, result: Result<Value, String>) {
        // A cancellation answers immediately; its eventual handler reply must be suppressed.
        if self.answered.lock().unwrap().remove(&Self::key(&id)) {
            return;
        }
        let value = match result {
            Ok(result) => json!({"jsonrpc":"2.0","id":id,"result":result}),
            Err(message) => {
                json!({"jsonrpc":"2.0","id":id,"error":{"code":-32000,"message":message}})
            }
        };
        self.send(value);
    }
    pub fn cancel_reply(&self, id: Value, result: Value) {
        if self.answered.lock().unwrap().insert(Self::key(&id)) {
            self.send(json!({"jsonrpc":"2.0","id":id,"result":result}));
        }
    }
}

#[derive(Clone)]
pub struct Host {
    pub rpc: Arc<Rpc>,
    pub session: String,
}
impl Host {
    pub async fn call(&self, method: &str, mut value: Value) -> Result<Value, String> {
        value["sessionId"] = json!(self.session);
        self.rpc.call(method, value).await
    }
    pub async fn status(&self, text: Value, tone: &str) {
        if let Err(error) = self
            .call(
                "ui.setStatus",
                json!({"key":"zgIndex","text":text,"tone":tone}),
            )
            .await
        {
            eprintln!("zvec-grep ui.setStatus: {error}");
        }
    }
}
